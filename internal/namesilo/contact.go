package namesilo

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"
)

// ContactInput contains the fields NameSilo requires before it will create a
// contact profile. Leaving a required field empty would permit a registration
// to fall back to the account's default profile, so AddContact rejects it.
type ContactInput struct {
	FirstName            string
	LastName             string
	Company              string
	Address              string
	Address2             string
	City                 string
	State                string
	Zip                  string
	Country              string
	Email                string
	Phone                string
	Fax                  string
	Nickname             string
	USNexusCategory      string
	USApplicationPurpose string
	EUCitizenship        string
}

type Contact struct {
	ID        string `json:"id"`
	Default   bool   `json:"default"`
	Nickname  string `json:"nickname,omitempty"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Company   string `json:"company,omitempty"`
	Address   string `json:"address"`
	Address2  string `json:"address2,omitempty"`
	City      string `json:"city"`
	State     string `json:"state"`
	Zip       string `json:"zip"`
	Country   string `json:"country"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	Fax       string `json:"fax,omitempty"`
}

type ContactResult struct {
	Environment string `json:"environment"`
	ContactID   string `json:"contact_id"`
}

type contactListReply struct {
	XMLName xml.Name `xml:"namesilo"`
	Request struct {
		Operation string `xml:"operation"`
	} `xml:"request"`
	Reply struct {
		Code     int `xml:"code"`
		Contacts []struct {
			ID string `xml:"contact_id"`
		} `xml:"contact"`
	} `xml:"reply"`
}

// SandboxContactIDs returns IDs of profiles already present in the sandbox.
// It is read-only and is intended for selecting a prevalidated fixture profile.
func (c *Client) SandboxContactIDs(ctx context.Context) ([]string, error) {
	var parsed contactListReply
	if err := c.readOperation(ctx, "contactList", nil, &parsed); err != nil || parsed.Request.Operation != "contactList" || parsed.Reply.Code != 300 {
		return nil, ErrInvalid
	}
	ids := make([]string, 0, len(parsed.Reply.Contacts))
	for _, contact := range parsed.Reply.Contacts {
		if !safeValue(contact.ID, 128) {
			return nil, ErrInvalid
		}
		ids = append(ids, contact.ID)
	}
	return ids, nil
}

func (i ContactInput) values() (url.Values, bool) {
	required := []string{i.FirstName, i.LastName, i.Address, i.City, i.State, i.Zip, i.Country, i.Email, i.Phone}
	for _, value := range required {
		if !safeValue(value, 128) {
			return nil, false
		}
	}
	for _, value := range []string{i.Company, i.Address2, i.Fax, i.Nickname, i.USNexusCategory, i.USApplicationPurpose, i.EUCitizenship} {
		if value != "" && !safeValue(value, 128) {
			return nil, false
		}
	}
	q := url.Values{"fn": {i.FirstName}, "ln": {i.LastName}, "ad": {i.Address}, "cy": {i.City}, "st": {i.State}, "zp": {i.Zip}, "ct": {i.Country}, "em": {i.Email}, "ph": {i.Phone}}
	for key, value := range map[string]string{"cp": i.Company, "ad2": i.Address2, "fx": i.Fax, "nn": i.Nickname, "usnc": i.USNexusCategory, "usap": i.USApplicationPurpose, "eucs": i.EUCitizenship} {
		if value != "" {
			q.Set(key, value)
		}
	}
	return q, true
}

// AddContact creates a fully specified sandbox profile. It is a mutation and
// deliberately uses the writer's one-shot, fresh-connection policy.
func (w *SandboxWriter) AddContact(ctx context.Context, input ContactInput) (ContactResult, error) {
	q, ok := input.values()
	if !ok || ctx.Err() != nil {
		return ContactResult{}, ErrInvalid
	}
	q.Set("version", "1")
	q.Set("type", "xml")
	q.Set("key", w.client.key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ote.namesilo.com/api/contactAdd?"+q.Encode(), nil)
	if err != nil {
		return ContactResult{}, ErrInvalid
	}
	req.Close = true
	call, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := w.client.http.Do(req.WithContext(call))
	if err != nil {
		return ContactResult{}, ErrOutcomeUnknown
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes || resp.StatusCode != http.StatusOK {
		return ContactResult{}, ErrOutcomeUnknown
	}
	var parsed struct {
		XMLName xml.Name `xml:"namesilo"`
		Request struct {
			Operation string `xml:"operation"`
		} `xml:"request"`
		Reply struct {
			Code int    `xml:"code"`
			ID   string `xml:"contact_id"`
		} `xml:"reply"`
	}
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	if dec.Decode(&parsed) != nil || dec.Decode(new(any)) != io.EOF || parsed.Request.Operation != "contactAdd" || parsed.Reply.Code != 300 || !safeValue(parsed.Reply.ID, 128) {
		return ContactResult{}, ErrOutcomeUnknown
	}
	return ContactResult{Environment: "sandbox", ContactID: parsed.Reply.ID}, nil
}

func safeValue(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) }) < 0
}
