package speaker

import (
	"strings"
	"testing"
	"time"
)

func TestHistoryGroupsFragmentsIntoTurns(t *testing.T) {
	now := time.Now()
	h := history{ttl: time.Hour}
	h.add(roleUser, "Какая ", now)
	h.add(roleUser, "погода?", now)
	h.add(roleAssistant, " Солнечно.", now)

	got := h.startInput(now)

	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2: %+v", len(got), got)
	}
	if got[0].Role != roleUser || got[0].Content[0].Type != "input_text" || got[0].Content[0].Text != "Какая погода?" {
		t.Errorf("first message = %+v", got[0])
	}
	if got[1].Role != roleAssistant || got[1].Content[0].Type != "output_text" ||
		got[1].Content[0].Text != "Солнечно." {
		t.Errorf("second message = %+v", got[1])
	}
}

func TestHistoryForgetsStaleConversations(t *testing.T) {
	now := time.Now()
	h := history{ttl: time.Hour}
	h.add(roleUser, "Привет", now)

	if got := h.startInput(now.Add(h.ttl + time.Minute)); len(got) != 0 {
		t.Fatalf("stale history was sent: %+v", got)
	}
}

func TestHistoryKeepsTheNewestTurnsWithinTheCharacterCap(t *testing.T) {
	now := time.Now()
	h := history{ttl: time.Hour}
	long := strings.Repeat("я", historyMaxChars/2+1)
	h.add(roleUser, "старое "+long, now)
	h.add(roleAssistant, "среднее "+long, now)
	h.add(roleUser, "новое", now)

	got := h.startInput(now)

	if len(got) != 2 || got[1].Content[0].Text != "новое" || !strings.HasPrefix(got[0].Content[0].Text, "среднее") {
		t.Fatalf("got %d messages, want the two newest within the cap", len(got))
	}
}
