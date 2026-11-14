package alarm_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// recordingWriter is a TicketWriter that records the created tickets.
type recordingWriter struct{ created []alarm.TicketRequest }

func (w *recordingWriter) Entitled(context.Context, string) bool { return true }

func (w *recordingWriter) Linked(context.Context, string, string, []string) (map[string][]alarm.TicketRef, error) {
	return nil, nil
}

func (w *recordingWriter) CreateTicket(_ context.Context, _ string, req *alarm.TicketRequest) (*alarm.TicketRef, error) {
	w.created = append(w.created, *req)
	return &alarm.TicketRef{TicketID: "55555555-5555-4555-8555-0000000010a5", TicketNumber: 1, Status: "new"}, nil
}

func (w *recordingWriter) LinkAndComment(context.Context, string, string, []alarm.TicketTarget, string) error {
	return nil
}

func (w *recordingWriter) ResolveTicket(context.Context, string, string, string, bool) error {
	return nil
}

// TestTicketTitleCutsByCharacter (WP-1040): the title of an alarm ticket
// from a summary of umlauts longer than 200 characters is cut at 200
// characters, never inside a UTF-8 sequence.
func TestTicketTitleCutsByCharacter(t *testing.T) {
	writer := &recordingWriter{}
	svc := alarm.NewService(alarm.NewMemoryStore(), nil, nil).WithTicketWriter(writer)
	actions := alarm.DefaultActions()
	actions.Ticket = &events.AlarmTicketAction{Enabled: true, ReporterID: "66666666-6666-4666-8666-0000000010a6"}
	in := ciRaise(cpeCI, "umlauts")
	in.Summary = "Größe überschritten: " + strings.Repeat("ä", 300)
	in.Actions = &actions
	a, err := svc.Raise(context.Background(), memOrg, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.OpenAlarmTicket(context.Background(), memOrg, a.ID); err != nil {
		t.Fatal(err)
	}
	if len(writer.created) != 1 {
		t.Fatalf("%d tickets, want one", len(writer.created))
	}
	title := writer.created[0].Title
	if !utf8.ValidString(title) || utf8.RuneCountInString(title) != 200 {
		t.Errorf("title of %d characters (valid UTF-8 %v), want 200", utf8.RuneCountInString(title), utf8.ValidString(title))
	}
}
