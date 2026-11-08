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
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

// waSender sends through a WhatsApp Web multi-device session (whatsmeow).
// The session lives in a SQLite file on the bridge volume.
type waSender struct {
	client *whatsmeow.Client
}

func newWASender(ctx context.Context, dbPath string) (*waSender, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite allows one writer; a pool makes whatsmeow's app-state sync hit SQLITE_BUSY
	container := sqlstore.NewWithDB(db, "sqlite3", waLog.Stdout("Store", "WARN", false))
	if err := container.Upgrade(ctx); err != nil {
		return nil, fmt.Errorf("upgrade session store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("load device: %w", err)
	}
	sender := &waSender{client: whatsmeow.NewClient(device, waLog.Stdout("WhatsApp", "WARN", false))}
	sender.client.AddEventHandler(func(evt interface{}) {
		switch evt.(type) {
		case *events.Disconnected:
			log.Printf("[WA BRIDGE] connection disconnected, auto-reconnecting...")
		case *events.Connected:
			log.Printf("[WA BRIDGE] connection established to WhatsApp network")
		case *events.LoggedOut:
			log.Printf("[WA BRIDGE] session logged out from mobile phone; re-pairing required")
		}
	})
	return sender, nil
}

// Connect connects the session; on first start it prints a high-contrast QR code to scan from WhatsApp
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
			if item.Event == whatsmeow.QRChannelEventCode {
				log.Printf("[WA BRIDGE] scan this high-contrast QR from WhatsApp > Linked devices (WhatsApp Business):")
				config := qrterminal.Config{
					Level:      qrterminal.M,
					Writer:     os.Stdout,
					HalfBlocks: false,
					BlackChar:  qrterminal.BLACK,
					WhiteChar:  qrterminal.WHITE,
					QuietZone:  4,
				}
				qrterminal.GenerateWithConfig(item.Code, config)
			} else if item == whatsmeow.QRChannelSuccess || item.Event == "success" {
				log.Printf("[WA BRIDGE] WhatsApp session paired and ready")
			} else {
				log.Printf("[WA BRIDGE] pairing event: %s (err: %v)", item.Event, item.Error)
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
