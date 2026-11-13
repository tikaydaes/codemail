package fetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// imapState is how far each folder has been read.
type imapState struct {
	Folders map[string]folderState `json:"folders"`
}

type folderState struct {
	Validity uint32 `json:"validity"`
	Next     uint32 `json:"next"` // the first UID not yet seen
}

// imapSession is one signed-in connection.
type imapSession struct {
	conn    net.Conn
	client  *imapclient.Client
	folders []string // the inbox and the junk folder
}

// Names providers give the junk folder when they do not mark it.
var junkNames = map[string]bool{"junk": true, "spam": true, "junk email": true, "junk e-mail": true,
	"bulk mail": true, "bulk": true, "junk mail": true}

// imapConnect signs in and finds the folders to watch. hostPort decides
// the encryption: port 993 is TLS from the start, anything else upgrades
// with STARTTLS, and plain text is only used when insecure is set.
func imapConnect(ctx context.Context, d *net.Dialer, hostPort, username, password string, insecure bool) (*imapSession, error) {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return nil, fmt.Errorf("the server address should look like imap.example.com:993")
	}
	conn, err := d.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return nil, friendly(err)
	}
	conn.SetDeadline(time.Now().Add(45 * time.Second))
	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	var client *imapclient.Client
	switch {
	case port == "993":
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("could not make a secure connection: %v", err)
		}
		client = imapclient.New(tc, nil)
	case insecure:
		client = imapclient.New(conn, nil)
	default:
		client, err = imapclient.NewStartTLS(conn, &imapclient.Options{TLSConfig: tlsCfg})
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("the server did not offer a secure connection: %v", err)
		}
	}
	s := &imapSession{conn: conn, client: client}
	if err := client.Login(username, password).Wait(); err != nil {
		s.close()
		return nil, fmt.Errorf("the mail server refused the sign-in: %v", imapText(err))
	}
	boxes, err := client.List("", "*", &imap.ListOptions{ReturnSpecialUse: client.Caps().Has(imap.CapSpecialUse)}).Collect()
	if err != nil {
		s.close()
		return nil, fmt.Errorf("could not list folders: %v", imapText(err))
	}
	s.folders = []string{"INBOX"}
	junk := ""
	for _, b := range boxes {
		last := b.Mailbox
		if b.Delim != 0 {
			if i := strings.LastIndex(last, string(b.Delim)); i >= 0 {
				last = last[i+1:]
			}
		}
		marked := false
		for _, a := range b.Attrs {
			if a == imap.MailboxAttrJunk {
				marked = true
			}
			if a == imap.MailboxAttrNoSelect || a == imap.MailboxAttrNonExistent {
				last = ""
			}
		}
		if marked || junk == "" && junkNames[strings.ToLower(last)] {
			junk = b.Mailbox
			if marked {
				break
			}
		}
	}
	if junk != "" {
		s.folders = append(s.folders, junk)
	}
	return s, nil
}

func (s *imapSession) close() {
	s.conn.SetDeadline(time.Now().Add(3 * time.Second))
	s.client.Logout().Wait()
	s.client.Close()
}

// poll returns the messages that arrived in the watched folders since
// state, and the new state. The first look at a folder only notes where
// it ends: mail from before the mailbox was linked is left alone.
func (s *imapSession) poll(state imapState, maxBytes int64) ([][]byte, imapState, error) {
	s.conn.SetDeadline(time.Now().Add(90 * time.Second))
	next := imapState{Folders: map[string]folderState{}}
	var out [][]byte
	for _, name := range s.folders {
		sel, err := s.client.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait()
		if err != nil {
			if name != "INBOX" {
				continue // a junk folder that has gone away is not an error
			}
			return nil, state, fmt.Errorf("could not open the inbox: %v", imapText(err))
		}
		was, known := state.Folders[name]
		now := folderState{Validity: sel.UIDValidity, Next: uint32(sel.UIDNext)}
		next.Folders[name] = now
		if !known || was.Validity != now.Validity || now.Next <= was.Next || sel.NumMessages == 0 {
			continue
		}
		first := was.Next
		if now.Next-first > maxPerPoll {
			first = now.Next - maxPerPoll // after a long gap, only the newest
		}
		var set imap.UIDSet
		set.AddRange(imap.UID(first), imap.UID(now.Next-1))
		sizes, err := s.client.Fetch(set, &imap.FetchOptions{UID: true, RFC822Size: true}).Collect()
		if err != nil {
			return nil, state, fmt.Errorf("could not read %s: %v", name, imapText(err))
		}
		var want imap.UIDSet
		for _, m := range sizes {
			if m.UID >= imap.UID(first) && m.RFC822Size <= maxBytes {
				want.AddNum(m.UID)
			}
		}
		if len(want) == 0 {
			continue
		}
		whole := &imap.FetchItemBodySection{Peek: true} // Peek: do not mark it read
		msgs, err := s.client.Fetch(want, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{whole}}).Collect()
		if err != nil {
			return nil, state, fmt.Errorf("could not read %s: %v", name, imapText(err))
		}
		for _, m := range msgs {
			if raw := m.FindBodySection(whole); len(raw) > 0 {
				out = append(out, raw)
			}
		}
	}
	return out, next, nil
}

// imapText is the server's own explanation, without the library's prefix.
func imapText(err error) string {
	var ie *imap.Error
	if errors.As(err, &ie) && ie.Text != "" {
		return ie.Text
	}
	return err.Error()
}

// friendly rewrites the errors a mistyped address produces.
func friendly(err error) error {
	var dns *net.DNSError
	switch {
	case errors.Is(err, errPrivate):
		return errPrivate
	case errors.As(err, &dns):
		return fmt.Errorf("there is no server called %s", dns.Name)
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("the server did not answer")
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Timeout() {
		return errors.New("the server did not answer")
	}
	return fmt.Errorf("could not connect: %v", err)
}
