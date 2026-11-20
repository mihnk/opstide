package notify

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/mihnk/opstide/internal/digest"
)

// payload is the body sent to a destination of this type.
func payload(kind string, m digest.Morning) ([]byte, error) {
	lines := m.Lines()
	var v any
	switch kind {
	case Slack:
		v = slackBody(lines)
	case Teams:
		v = teamsBody(lines)
	default:
		v = webhookBody{Schema: digest.Schema, Kind: "opstide.morning_summary", Text: plainText(lines), Lines: lines, Summary: m}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // Slack's links are <url|text>; nothing here is read as HTML
	err := enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n"), err
}

// Slack incoming webhooks take text (the notification's fallback) and blocks of mrkdwn.
type slackMessage struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks"`
}

type slackBlock struct {
	Type     string       `json:"type"`
	Text     *slackText   `json:"text,omitempty"`
	Elements []*slackText `json:"elements,omitempty"`
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// slackMax is under Slack's 3000 characters per section text.
const slackMax = 2900

func slackBody(lines []digest.Line) slackMessage {
	msg := slackMessage{}
	var section []string
	flush := func() {
		if len(section) > 0 {
			msg.Blocks = append(msg.Blocks, slackBlock{Type: "section", Text: &slackText{Type: "mrkdwn", Text: clip(strings.Join(section, "\n"), slackMax)}})
			section = nil
		}
	}
	for _, l := range lines {
		t := slackLine(l)
		switch l.Kind {
		case "heading":
			msg.Text = l.Plain()
			flush()
			section = append(section, t)
		case "note":
			flush()
			msg.Blocks = append(msg.Blocks, slackBlock{Type: "context", Elements: []*slackText{{Type: "mrkdwn", Text: clip(t, slackMax)}}})
		case "item":
			section = append(section, "• "+t)
		default:
			section = append(section, t)
		}
	}
	flush()
	return msg
}

func slackLine(l digest.Line) string {
	var b strings.Builder
	for _, p := range l.Parts {
		t := slackEscape(p.Text)
		switch {
		case p.Link != "":
			t = "<" + p.Link + "|" + t + ">"
		case p.Mono:
			t = "`" + strings.ReplaceAll(t, "`", "'") + "`"
		case p.Bold && strings.TrimSpace(t) != "":
			t = "*" + t + "*"
		}
		b.WriteString(t)
	}
	return b.String()
}

// slackEscape escapes the three characters Slack reads as markup.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// Teams workflow webhooks ("Post to a channel when a webhook request is received") take a message with one Adaptive
// Card attachment.
type teamsMessage struct {
	Type        string            `json:"type"`
	Attachments []teamsAttachment `json:"attachments"`
}

type teamsAttachment struct {
	ContentType string    `json:"contentType"`
	Content     teamsCard `json:"content"`
}

type teamsCard struct {
	Schema  string           `json:"$schema"`
	Type    string           `json:"type"`
	Version string           `json:"version"`
	Body    []teamsTextBlock `json:"body"`
}

type teamsTextBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Wrap     bool   `json:"wrap"`
	Weight   string `json:"weight,omitempty"`
	Size     string `json:"size,omitempty"`
	IsSubtle bool   `json:"isSubtle,omitempty"`
	Spacing  string `json:"spacing,omitempty"`
}

func teamsBody(lines []digest.Line) teamsMessage {
	card := teamsCard{Schema: "http://adaptivecards.io/schemas/adaptive-card.json", Type: "AdaptiveCard", Version: "1.4"}
	for _, l := range lines {
		b := teamsTextBlock{Type: "TextBlock", Text: teamsLine(l), Wrap: true}
		switch l.Kind {
		case "heading":
			b.Weight, b.Size = "Bolder", "Medium"
		case "note":
			b.IsSubtle, b.Size = true, "Small"
		case "item":
			b.Text, b.Spacing = "- "+b.Text, "None"
		}
		card.Body = append(card.Body, b)
	}
	return teamsMessage{Type: "message", Attachments: []teamsAttachment{{ContentType: "application/vnd.microsoft.card.adaptive", Content: card}}}
}

func teamsLine(l digest.Line) string {
	var b strings.Builder
	for _, p := range l.Parts {
		t := p.Text
		switch {
		case p.Link != "":
			t = "[" + strings.NewReplacer("[", "(", "]", ")").Replace(t) + "](" + p.Link + ")"
		case p.Bold && strings.TrimSpace(t) != "":
			t = "**" + t + "**"
		}
		b.WriteString(t)
	}
	return b.String()
}

// A webhook of your own gets the summary as data, and as text for whatever only shows text.
type webhookBody struct {
	Schema  int            `json:"schema"`
	Kind    string         `json:"kind"`
	Text    string         `json:"text"`
	Lines   []digest.Line  `json:"lines"`
	Summary digest.Morning `json:"summary"`
}

// plainText is the summary as plain text, with links after what they link.
func plainText(lines []digest.Line) string {
	var out []string
	for _, l := range lines {
		var b strings.Builder
		if l.Kind == "item" {
			b.WriteString("• ")
		}
		for _, p := range l.Parts {
			b.WriteString(p.Text)
			if p.Link != "" {
				b.WriteString(" <" + p.Link + ">")
			}
		}
		out = append(out, b.String())
	}
	return strings.Join(out, "\n")
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
