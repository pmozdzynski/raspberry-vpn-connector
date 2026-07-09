package handlers

import "testing"

func TestDetectTokenPromptFortinet(t *testing.T) {
	log := `POST https://secure.gibtele.com:20443/remote/logincheck
Enter token code or no code to send a notification to your FortiToken Mobile
Code:`

	prompt, ok := detectTokenPrompt(log)
	if !ok {
		t.Fatal("expected FortiToken prompt to be detected")
	}
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
}

func TestDetectTokenPromptCodeLine(t *testing.T) {
	log := `some earlier lines
Enter token code or no code to send a notification to your FortiToken Mobile
Code:`

	_, ok := detectTokenPrompt(log)
	if !ok {
		t.Fatal("expected prompt from Code: line context")
	}
}
