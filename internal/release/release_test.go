package release

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var t0 = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func template(image, mem string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api", "pod-template-hash": "abc", "secret-label": "do-not-keep", "app.kubernetes.io/version": "1.9.0"}},
		Spec: corev1.PodSpec{
			ServiceAccountName: "api",
			Containers: []corev1.Container{{
				Name: "api", Image: image,
				Env: []corev1.EnvVar{
					{Name: "DB_PASSWORD", Value: "hunter2-literal"},
					{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "api-secret"}, Key: "t"}}},
					{Name: "MODE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}, Key: "m"}}},
				},
				EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "bulk-secret"}}}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(mem)},
				},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/ready?token=abc123", Port: intstr.FromInt32(8080)}}, PeriodSeconds: 10},
				LivenessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"sh", "-c", "curl -u admin:pw localhost"}}}},
			}},
			Volumes: []corev1.Volume{
				{Name: "v1", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "files"}}}},
				{Name: "v2", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "tls"}}},
			},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}},
		},
	}
}

func TestSnapshotKeepsNamesAndSettingsButNoSecretsOrValues(t *testing.T) {
	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptrInt(3)}}
	s := FromTemplate(template("api:1.9.0", "256Mi"), d)
	raw := s.Encode()
	for _, leak := range []string{"hunter2-literal", "curl -u", "admin:pw", "abc123", "do-not-keep", "pod-template-hash"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("the snapshot leaks %q:\n%s", leak, raw)
		}
	}
	for _, want := range []string{`"DB_PASSWORD"`, `"api-secret"`, `"bulk-secret"`, `"tls"`, `"registry"`, `"api-config"`, `"files"`, `"memory":"256Mi"`, `"/ready"`, `"1.9.0"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %s:\n%s", want, raw)
		}
	}
	back, err := DecodeSnapshot(raw)
	if err != nil || back.Containers[0].Image != "api:1.9.0" || *back.Deployment.Replicas != 3 {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	var generic map[string]any
	_ = json.Unmarshal([]byte(raw), &generic)
	if _, ok := generic["env"]; ok {
		t.Fatal("an env field exists")
	}
}

func ptrInt(n int32) *int32 { return &n }

func TestFingerprintIgnoresOrderAndDeploymentFieldsButSeesTemplateChanges(t *testing.T) {
	a := FromTemplate(template("api:1", "256Mi"), nil)
	b := FromTemplate(template("api:1", "256Mi"), &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptrInt(9)}})
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("Deployment-level settings changed the template fingerprint (a rollback would not be recognised)")
	}
	if a.Fingerprint() == FromTemplate(template("api:2", "256Mi"), nil).Fingerprint() || a.Fingerprint() == FromTemplate(template("api:1", "512Mi"), nil).Fingerprint() {
		t.Fatal("a real change kept the fingerprint")
	}
	c := a
	c.Containers = append([]Container(nil), a.Containers...)
	c.Containers[0].EnvNames = []string{"TOKEN", "MODE", "DB_PASSWORD"} // another order
	if c.Fingerprint() != a.Fingerprint() {
		t.Fatal("order of names changed the fingerprint")
	}
}

func TestReleaseIDIsStableAndRollbackGetsItsOwn(t *testing.T) {
	first, again := ID("d", "rs1", "5"), ID("d", "rs1", "5")
	if first != again || first == ID("d", "rs1", "7") || first == ID("d", "rs2", "5") || first == ID("e", "rs1", "5") {
		t.Fatal("ids are not a function of deployment, replicaset and revision")
	}
}

func TestDiffNamesWhatChanged(t *testing.T) {
	before := FromTemplate(template("api:1.8.3", "256Mi"), &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptrInt(3)}})
	tpl := template("api:1.9.0", "384Mi")
	tpl.Spec.Containers[0].ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(8080)}}, PeriodSeconds: 5}
	tpl.Spec.Volumes = append(tpl.Spec.Volumes, corev1.Volume{Name: "v3", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "files-v2"}}}})
	after := FromTemplate(tpl, &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptrInt(4)}})
	got := map[string]Change{}
	for _, c := range Diff(before, after) {
		got[c.Field+"|"+c.Container] = c
	}
	for _, want := range []string{"image|api", "memory limit|api", "readiness probe|api", "replicas|", "ConfigMap reference|"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if c := got["image|api"]; c.Before != "api:1.8.3" || c.After != "api:1.9.0" || !strings.Contains(c.Summary(), "→") {
		t.Fatalf("%+v", c)
	}
	if c := got["ConfigMap reference|"]; c.Kind != "added" || c.After != "files-v2" {
		t.Fatalf("%+v", c)
	}
	if len(Diff(before, before)) != 0 {
		t.Fatal("identical snapshots differ")
	}
}

func TestDiffDoesNotInventDeploymentLevelChangesForAdoptedReleases(t *testing.T) {
	observed := FromTemplate(template("api:1", "256Mi"), &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: ptrInt(3)}})
	adopted := FromTemplate(template("api:1", "256Mi"), nil)
	if len(Diff(adopted, observed)) != 0 || DeploymentFieldsCompared(adopted, observed) {
		t.Fatal("a Deployment-level difference was made up from an unknown side")
	}
}

func dep(gen, obsGen int64, want, updated, avail int32, conds ...appsv1.DeploymentCondition) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: gen},
		Spec:       appsv1.DeploymentSpec{Replicas: &want},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: obsGen, Replicas: want, UpdatedReplicas: updated, ReadyReplicas: avail, AvailableReplicas: avail, Conditions: conds},
	}
}

func TestClassifyRollout(t *testing.T) {
	done := appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable", LastUpdateTime: metav1.NewTime(at(3))}
	for name, tc := range map[string]struct {
		d     *appsv1.Deployment
		state string
	}{
		"complete":                     {dep(2, 2, 3, 3, 3, done), StateCompleted},
		"half done":                    {dep(2, 2, 3, 2, 2, appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated"}), StateProgressing},
		"deadline":                     {dep(2, 2, 3, 1, 2, appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: "timed out"}), StateStalled},
		"quota":                        {dep(2, 2, 3, 1, 2, appsv1.DeploymentCondition{Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate", Message: "exceeded quota"}), StateFailed},
		"not yet seen":                 {dep(3, 2, 3, 3, 3, done), StateObserved},
		"complete but one unavailable": {dep(2, 2, 3, 3, 2, done), StateProgressing},
	} {
		got := ClassifyRollout(tc.d)
		if got.State != tc.state {
			t.Errorf("%s: %s (%s), want %s", name, got.State, got.Reason, tc.state)
		}
	}
	if r := ClassifyRollout(dep(2, 2, 3, 3, 3, done)); !r.CompletedAt.Equal(at(3)) {
		t.Fatalf("completion time %v", r.CompletedAt)
	}
}

func pod(name string, restart int32, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-" + "x", CreationTimestamp: metav1.NewTime(at(0))},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(at(1))}},
			ContainerStatuses: statuses,
		},
	}
}

func cs(name string, restarts int32, last *corev1.ContainerStateTerminated, waiting string) corev1.ContainerStatus {
	s := corev1.ContainerStatus{Name: name, RestartCount: restarts, LastTerminationState: corev1.ContainerState{Terminated: last}}
	if waiting != "" {
		s.State.Waiting = &corev1.ContainerStateWaiting{Reason: waiting}
	}
	return s
}

func types(sigs []Signal) string {
	var out []string
	for _, s := range sigs {
		out = append(out, s.Type+"/"+s.Key)
	}
	return strings.Join(out, ",")
}

func TestObserveRecordsCreationReadinessAndNotOneTimeTwice(t *testing.T) {
	p := pod("api-1", 0, cs("api", 0, nil, ""))
	st, sigs := Observe(nil, p, at(2), true, true)
	if got := types(sigs); !strings.HasPrefix(got, "created/") || !strings.Contains(got, "ready/") || len(sigs) != 2 {
		t.Fatalf("first sight: %s", got)
	}
	if !st.Ready || !st.ReadyAt.Equal(at(1)) {
		t.Fatalf("%+v", st)
	}
	// The same Pod again, as an informer would resend it: nothing new.
	_, sigs = Observe(&st, p, at(3), true, true)
	if len(sigs) != 0 {
		t.Fatalf("a repeat made entries: %s", types(sigs))
	}
	// Ready to NotReady is a transition; the Pending phase before readiness is not.
	p.Status.Conditions[0] = corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(at(5)), Reason: "ContainersNotReady"}
	st, sigs = Observe(&st, p, at(6), true, true)
	if len(sigs) != 1 || sigs[0].Type != "notready" || sigs[0].Severity != "warning" || !sigs[0].At.Equal(at(5)) {
		t.Fatalf("%v", sigs)
	}
	pending := pod("api-2", 0)
	pending.Status.Phase = corev1.PodPending
	pending.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(at(0))}}
	_, sigs = Observe(nil, pending, at(1), true, true)
	if types(sigs) != "created/" {
		t.Fatalf("a Pending Pod made %s", types(sigs))
	}
}

func TestObserveRestartsAreCountedOnceAndNeverGivenAMadeUpTime(t *testing.T) {
	end := metav1.NewTime(at(4))
	oom := &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: end}
	st, _ := Observe(nil, pod("api-1", 0, cs("api", 0, nil, "")), at(2), true, true)

	st, sigs := Observe(&st, pod("api-1", 1, cs("api", 1, oom, "")), at(5), true, true)
	if got := types(sigs); got != "restarted/api/1,oomkilled/api/"+jsonMs(at(4)) {
		t.Fatalf("%s", got)
	}
	if !sigs[0].At.Equal(at(4)) || sigs[0].Facts["exitCode"] != "137" {
		t.Fatalf("a one-step restart takes the previous run's end time: %+v", sigs[0])
	}
	// Seen again, and again after another informer resync: no new entries.
	for range 3 {
		var more []Signal
		st, more = Observe(&st, pod("api-1", 1, cs("api", 1, oom, "")), at(6), true, true)
		if len(more) != 0 {
			t.Fatalf("repeat: %s", types(more))
		}
	}
	// The counter jumped by 3 while OpsTide was blind: one entry, with the delta and no time.
	st, sigs = Observe(&st, pod("api-1", 4, cs("api", 4, &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(at(8))}, "")), at(9), true, true)
	if len(sigs) != 1 || sigs[0].Facts["delta"] != "3" || !sigs[0].At.IsZero() || !strings.Contains(sigs[0].Summary, "3 restarts since last seen") {
		t.Fatalf("%+v", sigs)
	}
	// A counter that went down (should not happen) is ignored, not a restart.
	_, sigs = Observe(&st, pod("api-1", 2, cs("api", 2, nil, "")), at(10), true, true)
	if len(sigs) != 0 {
		t.Fatalf("%s", types(sigs))
	}
}

func jsonMs(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func TestObserveWaitingReasonsOnceAndOnlyTheBadOnes(t *testing.T) {
	st, sigs := Observe(nil, pod("api-1", 0, cs("api", 0, nil, "ContainerCreating")), at(1), true, true)
	if types(sigs) != "created/,ready/"+jsonMs(at(1)) {
		t.Fatalf("a normal waiting reason made an entry: %s", types(sigs))
	}
	st, sigs = Observe(&st, pod("api-1", 0, cs("api", 0, nil, "CrashLoopBackOff")), at(2), true, true)
	if len(sigs) != 1 || sigs[0].Type != "waiting" || sigs[0].Facts["reason"] != "CrashLoopBackOff" {
		t.Fatalf("%v", sigs)
	}
	_, sigs = Observe(&st, pod("api-1", 0, cs("api", 0, nil, "CrashLoopBackOff")), at(3), true, true)
	if len(sigs) != 0 {
		t.Fatal("the same waiting reason was recorded twice")
	}
}

func TestPodOfAnAdoptedReleaseOnlySetsABaseline(t *testing.T) {
	oom := &corev1.ContainerStateTerminated{Reason: "OOMKilled", FinishedAt: metav1.NewTime(at(-60))}
	st, sigs := Observe(nil, pod("old", 5, cs("api", 5, oom, "")), at(0), false, true)
	if len(sigs) != 0 || !st.Quiet || st.Containers["api"].Restarts != 5 {
		t.Fatalf("%v %+v", sigs, st)
	}
	// Later: the same history is not replayed; a new restart is real.
	st, sigs = Observe(&st, pod("old", 5, cs("api", 5, oom, "")), at(1), true, true)
	if len(sigs) != 0 {
		t.Fatalf("replayed: %s", types(sigs))
	}
	_ = st
	for _, o := range st.Containers["api"].OOM {
		if !o.Observed.IsZero() {
			t.Fatal("a past OOMKill was recorded as an observation")
		}
	}
}

func TestEvictedPod(t *testing.T) {
	p := pod("api-1", 0)
	p.Status.Phase, p.Status.Reason = corev1.PodFailed, "Evicted"
	st, sigs := Observe(nil, p, at(1), true, true)
	if !st.Evicted || !st.Terminated || !strings.Contains(types(sigs), "evicted/") {
		t.Fatalf("%+v %v", st, sigs)
	}
}

func baseInput() Input {
	return Input{Origin: "observed", Rollout: StateCompleted, ObservedAt: at(0), CompletedAt: at(3), Window: 10 * time.Minute, Desired: 2, Now: at(20),
		Watched: func(time.Time, time.Time) bool { return true },
		Pods:    []PodInput{{Name: "api-1", State: PodState{Ready: true}}, {Name: "api-2", State: PodState{Ready: true}}}}
}

func TestAssessHealthyNeedsTheWindowToCloseWithNothingAgainstTheRelease(t *testing.T) {
	in := baseInput()
	if v := Assess(in); v.Health != HealthHealthy || !v.WindowEnd.Equal(at(13)) || len(v.Findings) != 0 {
		t.Fatalf("%+v", v)
	}
	in.Now = at(8) // briefly at the desired replicas is not enough
	if v := Assess(in); v.Health != HealthObserving || !strings.Contains(strings.Join(v.Reasons, " "), "Observing until") {
		t.Fatalf("%+v", v)
	}
	in = baseInput()
	in.CompletedAt, in.Rollout = time.Time{}, StateProgressing
	if v := Assess(in); v.Health != HealthObserving {
		t.Fatalf("%+v", v)
	}
}

func TestAssessCountsRealProblemsAndNotRollingUpdateChurn(t *testing.T) {
	// Churn that must not count: a Pod of the release that was Pending then
	// Ready, and one that went NotReady only because it was being deleted.
	in := baseInput()
	deleted := PodState{Ready: false, Deleting: true, DeletedAt: at(6), Transitions: []Transition{{At: at(1), Ready: true}, {At: at(6), Ready: false}}}
	in.Pods = append(in.Pods, PodInput{Name: "api-3", State: deleted})
	if v := Assess(in); v.Health != HealthHealthy {
		t.Fatalf("expected churn counted: %+v", v.Findings)
	}

	for name, mutate := range map[string]func(*PodState){
		"restart": func(s *PodState) {
			s.Containers = map[string]*ContainerObs{"api": {Restarts: 1, Restarted: []Restart{{Observed: at(5), Count: 1, Delta: 1, Reason: "Error"}}}}
		},
		"oom": func(s *PodState) {
			s.Containers = map[string]*ContainerObs{"api": {OOM: []OOMObs{{At: at(4), Observed: at(5)}}}}
		},
		"waiting": func(s *PodState) {
			s.Containers = map[string]*ContainerObs{"api": {Waiting: []WaitObs{{Reason: "ImagePullBackOff", Observed: at(2)}}}}
		},
		"notready": func(s *PodState) { s.Transitions = []Transition{{At: at(1), Ready: true}, {At: at(7), Ready: false}} },
		"evicted":  func(s *PodState) { s.Evicted = true },
	} {
		in := baseInput()
		mutate(&in.Pods[0].State)
		v := Assess(in)
		if v.Health != HealthDegraded || len(v.Findings) != 1 || !strings.Contains(strings.Join(v.Reasons, " "), "api-1") {
			t.Errorf("%s: %+v", name, v)
		}
	}
	// The same restart after the window closed is not this release's post-deployment problem.
	in = baseInput()
	in.Pods[0].State.Containers = map[string]*ContainerObs{"api": {Restarted: []Restart{{Observed: at(30), Count: 1, Delta: 1}}}}
	in.Now = at(40)
	if v := Assess(in); v.Health != HealthHealthy {
		t.Fatalf("a restart after the window counted: %+v", v)
	}
	// Availability below target for over a minute counts; a few seconds does not.
	in = baseInput()
	in.Episodes = []Episode{{Since: at(5), Until: at(5).Add(20 * time.Second)}}
	if v := Assess(in); v.Health != HealthHealthy {
		t.Fatalf("%+v", v)
	}
	in.Episodes = []Episode{{Since: at(5), Until: at(8)}}
	if v := Assess(in); v.Health != HealthDegraded {
		t.Fatalf("%+v", v)
	}
}

func TestAssessStalledAndFailedRolloutsAreDegradedNotHealthy(t *testing.T) {
	in := baseInput()
	in.CompletedAt, in.Rollout, in.RolloutNote = time.Time{}, StateStalled, "ProgressDeadlineExceeded: timed out"
	v := Assess(in)
	if v.Health != HealthDegraded || !strings.Contains(v.Reasons[0], "stalled") {
		t.Fatalf("%+v", v)
	}
}

func TestAssessSaysInsufficientEvidenceInsteadOfGuessing(t *testing.T) {
	adopted := baseInput()
	adopted.Origin = "adopted"
	if v := Assess(adopted); v.Health != HealthInsufficient || !strings.Contains(v.Reasons[0], "before OpsTide") {
		t.Fatalf("%+v", v)
	}
	gap := baseInput()
	gap.Watched = func(from, to time.Time) bool { return false }
	if v := Assess(gap); v.Health != HealthInsufficient || !strings.Contains(strings.Join(v.Evidence, " "), "without a gap") {
		t.Fatalf("%+v", v)
	}
	unknown := baseInput()
	unknown.Watched = nil
	if v := Assess(unknown); v.Health != HealthInsufficient {
		t.Fatalf("%+v", v)
	}
	nopods := baseInput()
	nopods.Pods = nil
	if v := Assess(nopods); v.Health != HealthInsufficient || !strings.Contains(strings.Join(v.Evidence, " "), "No Pod") {
		t.Fatalf("a completed release with no observed Pods was called healthy: %+v", v)
	}
	early := baseInput()
	early.CompletedAt, early.Rollout, early.SupersededAt = time.Time{}, StateSuperseded, at(2)
	if v := Assess(early); v.Health != HealthInsufficient || !strings.Contains(strings.Join(v.Evidence, " "), "before its rollout completed") {
		t.Fatalf("%+v", v)
	}
	// A real problem is reported even when the evidence is also incomplete.
	both := baseInput()
	both.Watched = func(time.Time, time.Time) bool { return false }
	both.Pods[0].State.Evicted = true
	if v := Assess(both); v.Health != HealthDegraded || !strings.Contains(strings.Join(v.Reasons, " "), "Evidence is also incomplete") {
		t.Fatalf("%+v", v)
	}
}

func TestAssessWindowLengthChangesTheVerdictDeterministically(t *testing.T) {
	in := baseInput()
	in.Pods[0].State.Containers = map[string]*ContainerObs{"api": {Restarted: []Restart{{Observed: at(9), Count: 1, Delta: 1}}}}
	in.Window = 5 * time.Minute // closes at 08:00+3 = at(8): the restart at at(9) is after it
	if v := Assess(in); v.Health != HealthHealthy {
		t.Fatalf("5 min: %+v", v)
	}
	in.Window = 10 * time.Minute
	if v := Assess(in); v.Health != HealthDegraded {
		t.Fatalf("10 min: %+v", v)
	}
	first, second := Assess(in), Assess(in)
	if first.Health != second.Health || strings.Join(first.Reasons, "|") != strings.Join(second.Reasons, "|") {
		t.Fatal("not deterministic")
	}
	if !ValidWindow(15) || ValidWindow(7) {
		t.Fatal("ValidWindow")
	}
}

// While the window is open nothing is final: missing evidence is noted, not decided, and
// only a real finding can change the verdict; the decision is made when the window closes.
func TestAssessDecidesInsufficientOnlyWhenTheWindowCloses(t *testing.T) {
	open := baseInput()
	open.Now = at(8) // window runs to at(13)
	open.Watched = func(time.Time, time.Time) bool { return false }
	v := Assess(open)
	if v.Health != HealthObserving || !strings.Contains(strings.Join(v.Reasons, " "), "already incomplete") {
		t.Fatalf("%+v", v)
	}
	open.Pods = nil // the Deployment reports complete before its Pods were processed
	open.Watched = func(time.Time, time.Time) bool { return true }
	if v := Assess(open); v.Health != HealthObserving {
		t.Fatalf("a Pod not yet read was called a verdict mid-window: %+v", v)
	}
	closed := open
	closed.Now = at(20)
	if v := Assess(closed); v.Health != HealthInsufficient {
		t.Fatalf("once the window closed with no Pod ever seen: %+v", v)
	}
}

// What happened to a Pod long after the window belongs to no verdict.
func TestAssessIgnoresLateEvictionsAndFailures(t *testing.T) {
	in := baseInput()
	in.Now = at(60 * 24)
	in.Pods[0].State.Evicted, in.Pods[0].State.EvictedAt = true, at(60*24-5)
	in.Pods[1].State.Terminated, in.Pods[1].State.TermReason, in.Pods[1].State.TermSeen = true, "Failed", at(60*24-5)
	if v := Assess(in); v.Health != HealthHealthy || len(v.Findings) != 0 {
		t.Fatalf("late events counted: %+v", v.Findings)
	}
	in.Pods[0].State.EvictedAt = at(6) // inside the window
	if v := Assess(in); v.Health != HealthDegraded {
		t.Fatalf("an eviction inside the window must count: %+v", v)
	}
}

// A rollout that stalled and later completed keeps counting against its release.
func TestAssessStalledThenCompletedIsNotClean(t *testing.T) {
	in := baseInput()
	in.Trouble = []Trouble{{State: StateStalled, Reason: "ProgressDeadlineExceeded", At: at(1)}}
	v := Assess(in)
	if v.Health != HealthDegraded || !strings.Contains(strings.Join(v.Reasons, " "), "was stalled") {
		t.Fatalf("%+v", v)
	}
}

// A Pod's earlier life, when it was not watched, is a baseline and nothing more.
func TestObserveWithoutHistoryIsABaselineForRestartsButStillRecordsTheEvents(t *testing.T) {
	oom := &corev1.ContainerStateTerminated{Reason: "OOMKilled", FinishedAt: metav1.NewTime(at(-5))}
	p := pod("api-1", 7, cs("api", 7, oom, "CrashLoopBackOff"))
	st, sigs := Observe(nil, p, at(0), true, false)
	if got := types(sigs); !strings.HasPrefix(got, "created/") || !strings.Contains(got, "ready/") || strings.Contains(got, "restarted") || strings.Contains(got, "oomkilled") || strings.Contains(got, "waiting") {
		t.Fatalf("history of an unwatched life was replayed: %s", got)
	}
	if st.Containers["api"].Restarts != 7 {
		t.Fatal("no baseline")
	}
	_, sigs = Observe(&st, pod("api-1", 8, cs("api", 8, oom, "")), at(1), true, true)
	if len(sigs) != 1 || sigs[0].Facts["delta"] != "1" {
		t.Fatalf("only the new restart counts: %v", sigs)
	}
}

// A rollback reuses a ReplicaSet whose Pods have a past. What they did a few seconds before the
// rollback was observed is the earlier release's; for a release seen as it came up, the same
// moment is still its own (a minute of slack covers the order the watches deliver in).
func TestAssessRollbackHasNoSlackBeforeItWasObserved(t *testing.T) {
	justBefore := at(0).Add(-30 * time.Second)
	in := baseInput()
	in.Pods[0].State.Containers = map[string]*ContainerObs{"api": {Restarts: 1, Restarted: []Restart{{Observed: justBefore, Count: 1, Delta: 1, Reason: "Error"}}}}
	if v := Assess(in); v.Health != HealthDegraded {
		t.Fatalf("a fresh release keeps its slack: %+v", v)
	}
	in.Rollback = true
	if v := Assess(in); v.Health != HealthHealthy || len(v.Findings) != 0 {
		t.Fatalf("a restart before the rollback belongs to the earlier release: %+v", v.Findings)
	}
	in.Pods[0].State.Containers["api"].Restarted[0].Observed = at(1) // after it began
	if v := Assess(in); v.Health != HealthDegraded {
		t.Fatalf("a restart after the rollback began counts: %+v", v)
	}
}
