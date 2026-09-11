// customer-portal is deliberately separate from the privileged operator console.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/portal"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	demo := flag.Bool("demo", false, "local disposable demo with synthetic credentials")
	listen := flag.String("listen", "127.0.0.1:8791", "listener address")
	origin := flag.String("origin", "http://127.0.0.1:8791", "exact browser origin")
	database := flag.String("database", "", "private portal SQLite path; never the deployer database")
	cert := flag.String("tls-cert", "", "HTTPS certificate")
	key := flag.String("tls-key", "", "HTTPS private key")
	smtpFile := flag.String("smtp-config", "", "private JSON SMTP settings")
	mailKeyFile := flag.String("mail-key-file", "", "private file containing 32-byte hex mail encryption key")
	managementFile := flag.String("test-billing-management-config", "", "private Stripe test customer portal settings")
	webhookFile := flag.String("test-webhook-secret-file", "", "private Stripe test webhook signing secret file")
	testBilling := flag.Bool("test-billing", false, "enable owner billing request API for a separately configured Stripe test worker")
	nodeProjectsFile := flag.String("node-projects", "", "private JSON mapping Node project IDs to assigned build settings and runtimes")
	publicationFile := flag.String("publication-sites", "", "private JSON mapping assigned static project IDs to HTTPS content origins")
	signup := flag.Bool("signup", false, "enable public signup when mail is configured")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	var webhookSecret string
	if *webhookFile != "" {
		if !*testBilling || *demo {
			return errors.New("test webhook requires --test-billing and non-demo HTTPS mode")
		}
		raw, e := privateFile(*webhookFile)
		if e != nil {
			return errors.New("test webhook signing secret unavailable")
		}
		webhookSecret = strings.TrimSpace(string(raw))
	}
	var demoMailDirectory string
	if *demo {
		host, _, err := net.SplitHostPort(*listen)
		if err != nil || host != "127.0.0.1" || *origin != "http://"+*listen {
			return errors.New("demo requires matching 127.0.0.1 listener and HTTP origin")
		}
		if *database != "" || *cert != "" || *key != "" || *smtpFile != "" || *mailKeyFile != "" {
			return errors.New("demo uses a disposable database and no TLS files")
		}
		dir, err := os.MkdirTemp("", "customer-portal-demo-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		*database = filepath.Join(dir, "portal.db")
		demoMailDirectory = filepath.Join(dir, "mail")
		if err = os.Mkdir(demoMailDirectory, 0700); err != nil {
			return err
		}
	} else if *database == "" || *cert == "" || *key == "" {
		return errors.New("non-demo mode requires --database, --origin with HTTPS, --tls-cert and --tls-key")
	}
	store, err := portal.Open(*database)
	if err != nil {
		return err
	}
	defer store.Close()
	if *demo {
		_, token, err := store.Register(context.Background(), "demo@example.test", "demo-only-password", "Demo workspace")
		if err != nil {
			return err
		}
		if err = store.Verify(context.Background(), token); err != nil {
			return err
		}
		fmt.Println("Disposable demo login: demo@example.test / demo-only-password")
	}
	var accountMail *portal.AccountMail
	var sender portal.MailSender
	var encryptionKey []byte
	if *demo {
		encryptionKey = make([]byte, 32)
		if _, err = rand.Read(encryptionKey); err != nil {
			return err
		}
		sender = demoMailSender{directory: demoMailDirectory}
		*signup = true
		fmt.Println("Demo emails saved privately in:", demoMailDirectory)
	} else if *smtpFile != "" || *mailKeyFile != "" {
		if *smtpFile == "" || *mailKeyFile == "" {
			return errors.New("mail requires both --smtp-config and --mail-key-file")
		}
		data, e := privateFile(*smtpFile)
		if e != nil {
			return e
		}
		var opts portal.SMTPOptions
		if e = json.Unmarshal(data, &opts); e != nil {
			return errors.New("invalid SMTP JSON")
		}
		sender, e = portal.NewSMTPSender(opts)
		if e != nil {
			return e
		}
		raw, e := privateFile(*mailKeyFile)
		if e != nil {
			return e
		}
		encryptionKey, e = hex.DecodeString(strings.TrimSpace(string(raw)))
		if e != nil {
			return errors.New("mail key must be hexadecimal")
		}
	}
	if sender != nil {
		accountMail, err = portal.NewAccountMail(store, encryptionKey, *origin, *demo)
		if err != nil {
			return err
		}
	}
	sites := map[string]string{}
	if *publicationFile != "" {
		raw, e := privateFile(*publicationFile)
		if e != nil {
			return e
		}
		if len(raw) > 16384 || json.Unmarshal(raw, &sites) != nil {
			return errors.New("invalid publication site mapping")
		}
	}
	nodeProjects := map[string]portal.NodeProjectConfig{}
	if *nodeProjectsFile != "" {
		raw, e := privateFile(*nodeProjectsFile)
		if e != nil {
			return e
		}
		if len(raw) > 65536 || json.Unmarshal(raw, &nodeProjects) != nil {
			return errors.New("invalid Node project mapping")
		}
	}
	var management *hostingbilling.Management
	if *managementFile != "" {
		if !*testBilling || *demo {
			return errors.New("billing management requires non-demo test billing")
		}
		raw, e := privateFile(*managementFile)
		if e != nil {
			return errors.New("billing management configuration unavailable")
		}
		var cfg struct {
			Secret        string            `json:"secret_key"`
			Success       string            `json:"success_url"`
			Cancel        string            `json:"cancel_url"`
			Plans         map[string]string `json:"plans"`
			Configuration string            `json:"configuration"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cfg) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("invalid billing management configuration")
		}
		if cfg.Success != *origin+"/billing/success" {
			return errors.New("billing management return URL must match the portal")
		}
		client, e := hostingbilling.NewTestClient(cfg.Secret, cfg.Success, cfg.Cancel, cfg.Plans)
		if e != nil {
			return e
		}
		defer client.Close()
		management, e = hostingbilling.NewManagement(client, cfg.Configuration)
		if e != nil {
			return e
		}
	}
	var managementProvider portal.BillingManagement
	if management != nil {
		managementProvider = management
	}
	handler, err := portal.NewHTTP(store, portal.HTTPOptions{NodeProjects: nodeProjects, BillingManagement: managementProvider, TestWebhookSecret: webhookSecret, TestBilling: *testBilling, Origin: *origin, Development: *demo, Mail: accountMail, Signup: *signup, PublicationSites: sites})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: handler, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if accountMail != nil {
		done := make(chan struct{})
		go func() {
			defer close(done)
			accountMail.Run(ctx, sender, func(error) { fmt.Fprintln(os.Stderr, "Account mail delivery failed; inspect queue health") })
		}()
		defer func() { stop(); <-done }()
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	fmt.Println("Customer portal:", *origin)
	if *demo {
		err = server.Serve(listener)
	} else {
		err = server.ServeTLS(listener, *cert, *key)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func privateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return nil, errors.New("settings must be private regular files (0600), at most 16 KiB")
	}
	return os.ReadFile(path)
}

type demoMailSender struct{ directory string }

func (s demoMailSender) Send(ctx context.Context, message portal.Mail) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.directory, message.ID+".txt"), []byte("To: "+message.To+"\nSubject: "+message.Subject+"\n\n"+message.Text), 0600)
}
