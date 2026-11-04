package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func kubeconfigFile(t *testing.T, dir, name, ctx, server string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := strings.NewReplacer("CTX", ctx, "SERVER", server).Replace(`apiVersion: v1
kind: Config
clusters:
- name: CTX
  cluster: {server: SERVER}
users:
- name: CTX
  user: {token: t}
contexts:
- name: CTX
  context: {cluster: CTX, user: CTX}
`)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A KUBECONFIG list with several files, one of them missing, is merged as
// kubectl merges it; the current context comes from the first file that
// sets one, and -context picks another.
func TestKubeConfigMergesAListLikeKubectl(t *testing.T) {
	dir := t.TempDir()
	a := kubeconfigFile(t, dir, "a.yaml", "prod", "https://prod.example.com")
	if err := os.WriteFile(a, append(mustRead(t, a), []byte("current-context: prod\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	b := kubeconfigFile(t, dir, "b.yaml", "test", "https://test.example.com")
	list := strings.Join([]string{a, filepath.Join(dir, "missing.yaml"), b}, string(os.PathListSeparator))

	cfg, name, err := kubeConfig(list, "")
	if err != nil {
		t.Fatal(err)
	}
	if name != "prod" || cfg.Host != "https://prod.example.com" {
		t.Fatalf("current context: %s %s", name, cfg.Host)
	}
	cfg, name, err = kubeConfig(list, "test")
	if err != nil {
		t.Fatal(err)
	}
	if name != "test" || cfg.Host != "https://test.example.com" {
		t.Fatalf("chosen context: %s %s", name, cfg.Host)
	}
	if _, _, err := kubeConfig(list, "nope"); err == nil {
		t.Fatal("unknown context accepted")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
