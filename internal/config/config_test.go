package config

import (
	"log/slog"
	"reflect"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(nil, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.DataDir != "/data" || c.RetentionDays != 30 || c.MaxEvents != 2_000_000 || c.LogLevel != slog.LevelInfo || c.Namespaces != nil {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestFlagsWinOverEnvironment(t *testing.T) {
	c, err := Load([]string{"-namespaces", "payments, data ,", "-retention-days", "7"},
		env(map[string]string{"OPSTIDE_NAMESPACES": "ignored", "OPSTIDE_DATA_DIR": "/var/opstide", "OPSTIDE_LOG_LEVEL": "debug"}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Namespaces, []string{"payments", "data"}) || c.RetentionDays != 7 || c.DataDir != "/var/opstide" || c.LogLevel != slog.LevelDebug {
		t.Fatalf("got %+v", c)
	}
}

func TestRejectsBadValues(t *testing.T) {
	for _, tc := range []struct {
		args []string
		env  map[string]string
	}{
		{env: map[string]string{"OPSTIDE_RETENTION_DAYS": "a week"}},
		{args: []string{"-retention-days", "0"}},
		{args: []string{"-max-events", "10"}},
		{args: []string{"-log-level", "loud"}},
		{args: []string{"-data-dir", ""}},
		{args: []string{"extra"}},
	} {
		if _, err := Load(tc.args, env(tc.env)); err == nil {
			t.Errorf("%v %v: expected an error", tc.args, tc.env)
		}
	}
}

func TestTokenSettings(t *testing.T) {
	c, err := Load([]string{"-token-file", "/etc/opstide/token"}, func(k string) string {
		if k == "OPSTIDE_TOKEN" {
			return "from-env"
		}
		return ""
	})
	if err != nil || c.TokenFile != "/etc/opstide/token" || c.Token != "from-env" {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = Load(nil, func(k string) string {
		if k == "OPSTIDE_TOKEN_FILE" {
			return "/t"
		}
		return ""
	})
	if err != nil || c.TokenFile != "/t" || c.Token != "" {
		t.Fatalf("%+v %v", c, err)
	}
}

// Node observation is cluster-scoped, so it is off unless asked for, with a flag or the environment, also when
// only some namespaces are watched.
func TestNodeObservationIsOffUnlessAskedFor(t *testing.T) {
	c, err := Load([]string{"-namespaces", "payments"}, env(nil))
	if err != nil || c.ObserveNodes {
		t.Fatalf("%v %+v", err, c)
	}
	if c, _ := Load(nil, env(map[string]string{"OPSTIDE_OBSERVE_NODES": "true"})); !c.ObserveNodes {
		t.Fatal("the environment variable")
	}
	if c, _ := Load([]string{"-observe-nodes"}, env(nil)); !c.ObserveNodes {
		t.Fatal("the flag")
	}
	if c, _ := Load(nil, env(map[string]string{"OPSTIDE_OBSERVE_NODES": "false"})); c.ObserveNodes {
		t.Fatal("false means off")
	}
}

func TestDemoFlag(t *testing.T) {
	c, err := Load([]string{"-demo"}, func(string) string { return "" })
	if err != nil || !c.Demo {
		t.Fatalf("%+v %v", c, err)
	}
	if c, _ := Load(nil, func(string) string { return "" }); c.Demo {
		t.Fatal("the demo is off unless asked for")
	}
}

func TestPublicURL(t *testing.T) {
	c, err := Load(nil, env(map[string]string{"OPSTIDE_PUBLIC_URL": " https://opstide.example.com/ops/ "}))
	if err != nil || c.PublicURL != "https://opstide.example.com/ops" {
		t.Fatalf("%q %v", c.PublicURL, err)
	}
	if c, err := Load([]string{"-public-url", "http://127.0.0.1:8080"}, env(nil)); err != nil || c.PublicURL != "http://127.0.0.1:8080" {
		t.Fatalf("%q %v", c.PublicURL, err)
	}
	for _, bad := range []string{"opstide.example.com", "ftp://x", "https://", "https://x/?a=b", "https://x/#top", "https://u:p@x", "javascript:alert(1)"} {
		if _, err := Load([]string{"-public-url", bad}, env(nil)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
