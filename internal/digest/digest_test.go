package digest

import (
	"strings"
	"testing"
	"time"
)

func text(ls []Line) string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Kind+": "+l.Plain())
	}
	return strings.Join(out, "\n")
}

func TestLinesSayWhatNeedsALookAndNothingElse(t *testing.T) {
	to := time.Date(2026, 11, 17, 5, 30, 0, 0, time.UTC) // 08:30 in Istanbul
	quiet := Morning{Cluster: "prod", From: Since(CoversSince18, to, time.FixedZone("x", 3*3600)), To: to, TimeZone: "Europe/Istanbul",
		Language: "en", Include: DefaultIncludes, Runs: Runs{Total: 4, Succeeded: 4}, Due: Due{Count: 2, NextName: "Report", NextAt: to.Add(time.Hour)}}
	got := text(quiet.Lines())
	for _, want := range []string{"heading: Morning summary · prod · since yesterday 18:00", "4 runs: 4 succeeded, 0 failed.", "text: Nothing needs a look.",
		"Due today: 2 more, next Report at 09:30.", "Times in Europe/Istanbul."} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	busy := quiet
	busy.Language, busy.Late, busy.Test = "tr", true, true
	busy.Failed = []Run{{Name: "Nightly backup", Namespace: "data", CronJob: "nightly-backup", At: to.Add(-2 * time.Hour), Maintenance: "Upgrade"}}
	busy.Overdue = []Overdue{{Name: "Nightly backup", WithinSeconds: 26 * 3600, LastSuccess: to.Add(-30 * time.Hour)}}
	busy.Troubles = []Trouble{{Summary: "Pod api restarted (OOMKilled)", At: to.Add(-time.Hour), ChangesBefore: 2, Nearest: &Change{Summary: "x", Writer: "kubectl-edit", SecondsBefore: 420}}}
	busy.NotWatched = []string{"Kubernetes Events"}
	busy.Include = AllIncludes
	busy.ChangeList, busy.ChangesMore = []Change{{Summary: "Deployment shop/api written", At: to.Add(-90 * time.Minute)}}, 3
	got = text(busy.Lines())
	for _, want := range []string{"Deneme mesajı · Sabah özeti", "Geç gönderildi", "Başarısız Nightly backup data/nightly-backup, 06:30, Upgrade bakımı sırasında",
		"Gecikti Nightly backup: her 26 sa içinde başarılı olmalı, son başarı 30 sa önce",
		"kubectl-edit tarafından yapılan bir değişiklikten 7 dk sonra (önceki bir saatte 2 değişiklik)", "Değişti Deployment shop/api written, 07:00",
		"…ve 3 tane daha", "izlenmedi: Kubernetes Events"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "Bakılması gereken bir şey yok") {
		t.Error("a busy night is not called quiet")
	}
}

func TestSinceIsSixPmTheDayBefore(t *testing.T) {
	ist, _ := time.LoadLocation("Europe/Istanbul")
	at := time.Date(2026, 11, 17, 5, 30, 0, 0, time.UTC)
	if got := Since(CoversSince18, at, ist); !got.Equal(time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("since-18h %v", got)
	}
	if got := Since(Covers12h, at, ist); !got.Equal(at.Add(-12 * time.Hour)) {
		t.Errorf("12h %v", got)
	}
}
