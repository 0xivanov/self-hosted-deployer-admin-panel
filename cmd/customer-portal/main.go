// customer-portal is deliberately separate from the privileged operator console.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/domainbilling"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/fleetlogs"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/githubdeploy"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/hostingbilling"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/merchantbilling"
	"github.com/0xivanov/self-hosted-deployer-admin-panel/internal/namesilo"
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
	sandboxDomainFile := flag.String("sandbox-domain-config", "", "private NameSilo OTE JSON: secret_key and markup_minor; no real registrations")
	merchantFile := flag.String("test-merchant-config", "", "private Stripe test Connect settings with secret_key and countries")
	merchantConfig := flag.String("merchant-config", "", "private Stripe Connect settings for the selected merchant mode")
	managementFile := flag.String("test-billing-management-config", "", "private Stripe test customer portal settings")
	merchantWebhookFile := flag.String("test-merchant-webhook-secret-file", "", "Private Stripe Connect test webhook signing secret")
	merchantMode := flag.String("merchant-mode", "", "merchant payments: test or live")
	merchantWebhookSecretFile := flag.String("merchant-webhook-secret-file", "", "private merchant webhook signing secret for the selected mode")
	webhookFile := flag.String("test-webhook-secret-file", "", "private Stripe test webhook signing secret file")
	billingMode := flag.String("billing-mode", "", "hosting payments: disabled (empty), test or live")
	genericWebhookFile := flag.String("billing-webhook-secret-file", "", "private webhook signing secret for the selected hosting payment mode")
	genericManagementFile := flag.String("billing-management-config", "", "private customer billing portal settings for the selected mode")
	testBilling := flag.Bool("test-billing", false, "enable owner billing request API for a separately configured Stripe test worker")
	containerCredentialKey := flag.String("container-credential-key-file", "", "owner-private 32-byte hex key shared with the fleet worker for encrypted registry access and environment settings")
	containerHosting := flag.Bool("container-hosting", false, "enable experimental registry image hosting after fleet qualification")
	containerProjectsFile := flag.String("container-projects", "", "private JSON mapping container project IDs to operator-assigned runtimes")
	nodeProjectsFile := flag.String("node-projects", "", "private JSON mapping Node project IDs to assigned build settings and runtimes")
	publicationFile := flag.String("publication-sites", "", "private JSON mapping assigned static project IDs to HTTPS content origins")
	signup := flag.Bool("signup", false, "enable public signup when mail is configured")
	signupAllowlist := flag.String("signup-allowlist", "", "private JSON email allowlist for invitation-only signup")
	domainDNS := flag.String("domain-dns-resolver", "", "optional IP:port resolver for public custom-domain verification only")
	projectCapacity := flag.Int("hosting-project-capacity", 0, "total fleet project slots, including queued and deleting projects; 0 disables admission cap")
	nodeCapacity := flag.Int("hosting-node-capacity", 0, "Node project slots within total fleet capacity")
	runtimeLogs := flag.String("runtime-log-socket", "", "private fleet log bridge Unix socket")
	githubAutoDeploy := flag.Bool("github-auto-deploy", false, "enable GitHub push build and publication through configured workers")
	githubConfig := flag.String("github-config", "", "private JSON GitHub App settings with client_id, private_key_pem and client_secret")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	selectedBillingMode, selectedWebhookFile, selectedManagementFile, err := resolveBillingSettings(*billingMode, *testBilling, *webhookFile, *managementFile, *genericWebhookFile, *genericManagementFile, *demo)
	if err != nil {
		return err
	}
	*webhookFile = selectedWebhookFile
	*managementFile = selectedManagementFile
	selectedMerchantMode, selectedMerchantConfig, selectedMerchantWebhookFile, err := resolveMerchantSettings(*merchantMode, *merchantFile, *merchantWebhookFile, *merchantConfig, *merchantWebhookSecretFile, *demo)
	if err != nil {
		return err
	}
	*merchantFile = selectedMerchantConfig
	*merchantWebhookFile = selectedMerchantWebhookFile
	var githubWebhookSecret string
	var githubApp *githubdeploy.App
	var githubOAuth *githubdeploy.OAuth
	if *githubConfig != "" {
		if *demo {
			return errors.New("GitHub setup is unavailable in demo mode")
		}
		raw, e := privateFile(*githubConfig)
		if e != nil {
			return errors.New("GitHub configuration unavailable")
		}
		var cfg struct {
			ClientID      string `json:"client_id"`
			PrivateKeyPEM string `json:"private_key_pem"`
			ClientSecret  string `json:"client_secret"`
			WebhookSecret string `json:"webhook_secret"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF {
			return errors.New("invalid GitHub configuration")
		}
		githubWebhookSecret = cfg.WebhookSecret
		githubApp, e = githubdeploy.NewApp(cfg.ClientID, []byte(cfg.PrivateKeyPEM))
		if e != nil {
			return errors.New("invalid GitHub App configuration")
		}
		githubOAuth, e = githubdeploy.NewOAuth(githubApp, cfg.ClientSecret, *origin+"/github/callback")
		if e != nil {
			return errors.New("invalid GitHub OAuth configuration")
		}
	}
	var merchantWebhookSecret string
	if *merchantWebhookFile != "" {
		if *merchantFile == "" || *demo {
			return errors.New("merchant webhook requires merchant configuration and HTTPS mode")
		}
		raw, e := privateFile(*merchantWebhookFile)
		if e != nil {
			return errors.New("merchant webhook signing secret unavailable")
		}
		merchantWebhookSecret = strings.TrimSpace(string(raw))
		if merchantWebhookSecret == "" {
			return errors.New("merchant webhook signing secret empty")
		}
	}
	var webhookSecret string
	if *webhookFile != "" {
		if selectedBillingMode == "" || *demo {
			return errors.New("billing webhook requires billing and non-demo HTTPS mode")
		}
		raw, e := privateFile(*webhookFile)
		if e != nil {
			return errors.New("billing webhook signing secret unavailable")
		}
		webhookSecret = strings.TrimSpace(string(raw))
		if webhookSecret == "" {
			return errors.New("billing webhook signing secret is empty")
		}
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
	storeBillingMode := selectedBillingMode
	if storeBillingMode == "" {
		storeBillingMode = "test"
	}
	store, err := portal.OpenWithPaymentModes(*database, storeBillingMode, selectedMerchantMode)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.ConfigureProjectCapacity(*projectCapacity, *nodeCapacity); err != nil {
		return err
	}
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
	var publicationSites *portal.PublicationSitesProvider
	if *publicationFile != "" {
		publicationSites, err = portal.NewPublicationSitesProvider(*publicationFile, *origin)
		if err != nil {
			return err
		}
	}
	nodeProjects := map[string]portal.NodeProjectConfig{}
	var nodeProjectProvider *portal.NodeProjectProvider
	if *nodeProjectsFile != "" {
		nodeProjectProvider, err = portal.NewNodeProjectProvider(*nodeProjectsFile)
		if err != nil {
			return err
		}
		nodeProjects = nodeProjectProvider.Snapshot()
	}
	var containerProjectProvider *portal.ContainerProjectProvider
	if *containerHosting {
		if *containerProjectsFile == "" {
			return errors.New("container hosting requires an operator project mapping")
		}
		containerProjectProvider, err = portal.NewContainerProjectProvider(*containerProjectsFile)
		if err != nil {
			return err
		}
	} else if *containerProjectsFile != "" {
		return errors.New("container project mapping requires container hosting to be enabled")
	}
	var management *hostingbilling.Management
	if *managementFile != "" {
		if selectedBillingMode == "" || *demo {
			return errors.New("billing management requires non-demo billing")
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
		constructor := hostingbilling.NewTestClient
		if selectedBillingMode == "live" {
			constructor = hostingbilling.NewLiveClient
		}
		client, e := constructor(cfg.Secret, cfg.Success, cfg.Cancel, cfg.Plans)
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
	var domainPurchases *portal.DomainPurchases
	var domainReader portal.DomainQuoteReader
	var domainMarkup int64
	if *sandboxDomainFile != "" {
		raw, e := privateFile(*sandboxDomainFile)
		if e != nil {
			return errors.New("sandbox domain configuration unavailable")
		}
		var cfg struct {
			Secret    string `json:"secret_key"`
			Markup    int64  `json:"markup_minor"`
			Checkout  bool   `json:"checkout_enabled"`
			StripeKey string `json:"stripe_secret_key"`
			Contact   string `json:"contact_id"`
			Target    string `json:"target_ip"`
			Attempts  string `json:"attempts_directory"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF || cfg.Markup < 0 {
			return errors.New("invalid sandbox domain configuration")
		}
		client, e := namesilo.NewSandboxClient(cfg.Secret)
		if e != nil {
			return e
		}
		defer client.Close()
		domainReader = client
		domainMarkup = cfg.Markup
		if cfg.Checkout {
			if cfg.Markup != 0 || cfg.Contact == "" || net.ParseIP(cfg.Target) == nil || !filepath.IsAbs(cfg.Attempts) {
				return errors.New("invalid sandbox checkout settings")
			}
			if err = os.MkdirAll(cfg.Attempts, 0700); err != nil {
				return err
			}
			info, e := os.Lstat(cfg.Attempts)
			if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
				return errors.New("sandbox attempts directory must be private")
			}
			payment, e := domainbilling.NewTestClient(cfg.StripeKey, *origin+"/?domain_checkout=return", *origin+"/?domain_checkout=return")
			if e != nil {
				return e
			}
			defer payment.Close()
			writer, e := namesilo.NewSandboxWriter(cfg.Secret)
			if e != nil {
				return e
			}
			defer writer.Close()
			domainPurchases = portal.NewDomainPurchases(store, client, payment, &sandboxRegistrar{reader: client, writer: writer, contact: cfg.Contact, directory: cfg.Attempts, target: cfg.Target})
		}

	}
	var merchantProvider portal.MerchantProvider
	var merchantCountries []string
	if *merchantFile != "" {
		if *demo {
			return errors.New("merchant setup requires non-demo HTTPS mode")
		}
		raw, e := privateFile(*merchantFile)
		if e != nil {
			return errors.New("merchant configuration unavailable")
		}
		var cfg struct {
			SecretKey string   `json:"secret_key"`
			Countries []string `json:"countries"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF {
			return errors.New("invalid merchant configuration")
		}
		constructor := merchantbilling.NewTestClient
		if selectedMerchantMode == "live" {
			constructor = merchantbilling.NewLiveClient
		}
		client, e := constructor(cfg.SecretKey, *origin+"/merchant/return", *origin+"/merchant/refresh", cfg.Countries)
		if e != nil {
			return e
		}
		defer client.Close()
		merchantProvider = client
		merchantCountries = cfg.Countries
	}
	var signupAllowed func(string) bool
	if *signupAllowlist != "" {
		if *demo {
			return errors.New("signup allowlist is unavailable in demo mode")
		}
		signupAllowed = portal.SignupAllowlist(*signupAllowlist)
	}
	opts := portal.HTTPOptions{DomainPurchases: domainPurchases, DomainQuotes: domainReader, DomainMarkupMinor: domainMarkup, MerchantMode: selectedMerchantMode, MerchantWebhookSecret: merchantWebhookSecret, Merchant: merchantProvider, MerchantCountries: merchantCountries, NodeProjects: nodeProjects, BillingManagement: managementProvider, BillingWebhookSecret: webhookSecret, BillingMode: selectedBillingMode, Origin: *origin, Development: *demo, Mail: accountMail, Signup: *signup, SignupAllowed: signupAllowed}
	if githubApp != nil {
		opts.GitHubApp = githubApp
		opts.GitHubOAuth = githubOAuth
		opts.GitHubWebhookSecret = githubWebhookSecret
	}
	opts.GitHubAutoDeploy = *githubAutoDeploy
	opts.ContainerHosting = *containerHosting
	if *containerCredentialKey != "" {
		if !*containerHosting {
			return errors.New("container secrets require --container-hosting")
		}
		key, err := portal.LoadContainerCredentialKey(*containerCredentialKey)
		if err != nil {
			return err
		}
		opts.ContainerCredentials, err = portal.NewContainerCredentials(store, key)
		if err != nil {
			return err
		}
		opts.ContainerEnvironments, err = portal.NewContainerEnvironments(store, key)
		if err != nil {
			return err
		}
	}
	if containerProjectProvider != nil {
		opts.ContainerProjectLookup = containerProjectProvider.Snapshot
	}
	if *runtimeLogs != "" {
		if !filepath.IsAbs(*runtimeLogs) {
			return errors.New("runtime log socket must be absolute")
		}
		opts.RuntimeLogs = fleetlogs.Reader(*runtimeLogs)
	}
	if nodeProjectProvider != nil {
		opts.NodeProjectLookup = nodeProjectProvider.Snapshot
	}
	if publicationSites != nil {
		opts.PublicationSitesLookup = publicationSites.Snapshot
	}
	if *domainDNS != "" {
		opts.CustomDomainResolver, err = portal.CustomDomainDNS(*domainDNS)
		if err != nil {
			return err
		}
	}
	handler, err := portal.NewHTTP(store, opts)
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
	if domainPurchases != nil {
		done := make(chan struct{})
		go func() { defer close(done); domainPurchases.Run(ctx) }()
		defer func() { stop(); <-done }()
	}
	if githubApp != nil {
		done := make(chan struct{})
		go func() {
			defer close(done)
			var nextGitHubCleanup time.Time
			timer := time.NewTicker(2 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					if time.Now().After(nextGitHubCleanup) {
						if _, err := store.PruneGitHubHistory(ctx); err != nil && ctx.Err() == nil {
							fmt.Fprintln(os.Stderr, "GitHub history cleanup could not complete")
						}
						nextGitHubCleanup = time.Now().Add(time.Hour)
					}
					if opts.GitHubWebhookSecret != "" {
						if _, err := store.WorkGitHubPush(ctx, githubApp); err != nil && ctx.Err() == nil {
							fmt.Fprintln(os.Stderr, "GitHub push could not advance; inspect push status")
						}
					}
					if _, err := handler.WorkGitHubPipeline(ctx); err != nil && ctx.Err() == nil {
						fmt.Fprintln(os.Stderr, "GitHub deployment could not advance; inspect deployment status")
					}
					if _, err := store.WorkGitHubImport(ctx, githubApp); err != nil && ctx.Err() == nil {
						fmt.Fprintln(os.Stderr, "GitHub import could not advance; inspect import status")
					}
				}
			}
		}()
		defer func() { stop(); <-done }()
	}
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
