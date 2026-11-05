package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

// waSender sends through a WhatsApp Web multi-device session (whatsmeow, unofficial; demo and
// development only, decision W12). The session lives in a SQLite file on the bridge volume.
type waSender struct {
	client *whatsmeow.Client
}

func newWASender(ctx context.Context, dbPath string) (*waSender, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	container := sqlstore.NewWithDB(db, "sqlite3", waLog.Stdout("Store", "WARN", false))
	if err := container.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("upgrade session store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("load device: %w", err)
	}
	return &waSender{client: whatsmeow.NewClient(device, waLog.Stdout("WhatsApp", "WARN", false))}, nil
}

// Connect connects the session; on first start it prints a QR code to scan from the demo phone
// (WhatsApp > Linked devices > Link a device).
func (s *waSender) Connect(ctx context.Context) error {
	if s.client.Store.ID != nil {
		return s.client.Connect()
	}
	qr, err := s.client.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := s.client.Connect(); err != nil {
		return err
	}
	go func() {
		for item := range qr {
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				log.Printf("[WA BRIDGE] scan this QR from WhatsApp > Linked devices on the demo phone:")
				qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stdout)
			case "success":
				log.Printf("[WA BRIDGE] paired")
			default:
				log.Printf("[WA BRIDGE] pairing: %s", item.Event)
			}
		}
	}()
	return nil
}

func (s *waSender) Ready() bool { return s.client.IsConnected() && s.client.IsLoggedIn() }

func (s *waSender) Send(ctx context.Context, phone, text string) error {
	if !s.Ready() {
		return ErrNotReady
	}
	found, err := s.client.IsOnWhatsApp(ctx, []string{phone})
	if err != nil {
		return fmt.Errorf("lookup recipient: %w", err)
	}
	if len(found) == 0 || !found[0].IsIn {
		return ErrNotOnWhatsApp
	}
	_, err = s.client.SendMessage(ctx, found[0].JID, &waE2E.Message{Conversation: proto.String(text)})
	return err
}

func (s *waSender) Close() { s.client.Disconnect() }
