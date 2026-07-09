package handlers

import (
	"testing"
	"time"
)

func TestDetectTokenPromptIgnoresPasswordLine(t *testing.T) {
	log := `POST https://secure.gibtele.com:20443/remote/logincheck
Password:`
	if _, ok := detectTokenPrompt(log); ok {
		t.Fatal("expected password prompt not to match token detector")
	}
}

func TestDetectTokenPromptMatchesFortiToken(t *testing.T) {
	log := `POST https://example/remote/logincheck
Please enter your one-time password:`
	if _, ok := detectTokenPrompt(log); !ok {
		t.Fatal("expected token prompt")
	}
}

func TestDetectPasswordRetryPrompt(t *testing.T) {
	log := `POST https://secure.gibtele.com:20443/remote/logincheck
Password:`
	sentAt := time.Now().Add(-5 * time.Second)
	if _, ok := detectPasswordRetryPrompt(log, sentAt); !ok {
		t.Fatal("expected password retry prompt")
	}
}

func TestDetectPasswordRetryPromptTooSoon(t *testing.T) {
	log := `Password:`
	sentAt := time.Now().Add(-500 * time.Millisecond)
	if _, ok := detectPasswordRetryPrompt(log, sentAt); ok {
		t.Fatal("expected no retry prompt immediately after send")
	}
}
