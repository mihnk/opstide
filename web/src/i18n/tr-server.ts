// Turkish for the sentences the API writes for display: notes, explanations, coverage notes, relationship and
// assessment texts. They are fixed English text from OpsTide (internal/api, internal/release, internal/evidence,
// internal/workload, internal/collector), not cluster data; the screens show them through ts() in ../i18n.ts.
//
// A key with {name} parts is a sentence the server builds around values: each part matches the value as written. Only
// {note} (another of these sentences), {label} and {state} (an interface word) and {labels} (a comma-separated list of
// them) are translated in turn; every other part (a name, a Kubernetes reason, a time, a number) stays as it is. Keys are
// tried in the order written here, so a longer form of a sentence comes before a shorter one it would also match.
export const server: Record<string, string> = {
  // ---- Explorer record drawer: observation, provenance, coverage (internal/api/explorer_detail.go)
  "The time Kubernetes reported for the Event's first occurrence. OpsTide first saw it at the observed time.":
    "Kubernetes'in Event'in ilk gerçekleşmesi için bildirdiği zaman. OpsTide onu ilk olarak gözlem zamanında gördü.",
  "Kubernetes gave this Event no time, so the time shown is when OpsTide first saw it.":
    "Kubernetes bu Event için bir zaman vermedi; bu yüzden gösterilen zaman, OpsTide'ın onu ilk gördüğü andır.",
  "The source gave no time for this Event; its position on the timeline is when OpsTide first saw it, not when it happened.":
    "Kaynak bu Event için bir zaman vermedi; zaman çizelgesindeki yeri, gerçekleştiği an değil, OpsTide'ın onu ilk gördüğü andır.",
  "Reported by Kubernetes and stored as reported. OpsTide does not know why the Event was emitted.":
    "Kubernetes tarafından bildirildi ve bildirildiği gibi saklandı. OpsTide, Event'in neden üretildiğini bilmez.",
  "This Event was already present when OpsTide began watching; when it was first emitted may be earlier than when OpsTide saw it.":
    "Bu Event, OpsTide izlemeye başladığında zaten vardı; ilk üretildiği an, OpsTide'ın onu gördüğü andan daha önce olabilir.",
  "The message was longer than OpsTide stores and was cut.": "Mesaj, OpsTide'ın sakladığı uzunluktan fazlaydı ve kısaltıldı.",
  "Kubernetes has since dropped this Event; OpsTide's copy is what remains.": "Kubernetes bu Event'i sonradan sildi; geriye OpsTide'daki kopyası kaldı.",
  "A Kubernetes Event is the cluster's own report. Other records near it in time are not thereby related to it.":
    "Bir Kubernetes Event'i, cluster'ın kendi bildirimidir. Zamanca ona yakın başka kayıtlar bu yüzden onunla ilişkili sayılmaz.",
  "The time the source gives for it.": "Kaynağın bunun için verdiği zaman.",
  "The source gave no time, so the time shown is when OpsTide first observed it.":
    "Kaynak bir zaman vermedi; bu yüzden gösterilen zaman, OpsTide'ın onu ilk gözlediği andır.",
  "The source gave no time for this; its position on the timeline is when OpsTide observed it, not necessarily when it happened.":
    "Kaynak bunun için bir zaman vermedi; zaman çizelgesindeki yeri, OpsTide'ın onu gözlediği andır ve gerçekleştiği an olmayabilir.",
  "A documented rule of OpsTide applied to recorded observations. It is a result, not an observation.":
    "OpsTide'ın belgelenmiş bir kuralının kaydedilmiş gözlemlere uygulanmasıdır. Bir gözlem değil, bir sonuçtur.",
  "OpsTide records that it does not know this.": "OpsTide bunu bilmediğini kaydeder.",
  "Read from Kubernetes or from OpsTide's own watching, and recorded as seen.":
    "Kubernetes'ten ya da OpsTide'ın kendi izlemesinden okundu ve görüldüğü gibi kaydedildi.",
  "The rule-set version is the one stored with the release when it was assessed.":
    "Kural seti sürümü, release değerlendirildiğinde onunla birlikte saklanan sürümdür.",
  "This rule is part of OpsTide {version} and is not versioned separately; the inputs below are what it was given.":
    "Bu kural OpsTide {version} sürümünün bir parçasıdır ve ayrıca sürümlenmez; aşağıdaki girdiler ona verilenlerdir.",
  "Current state — what OpsTide knows now, not the state at this record's time":
    "Güncel durum — kaydın zamanındaki durum değil, OpsTide'ın şu an bildiği",
  "This record is OpsTide's own account of its watching.": "Bu kayıt, OpsTide'ın kendi izlemesine dair tuttuğu kayıttır.",
  "Node observation is off, so nothing says what the Nodes were doing then.":
    "Node gözlemi kapalı; bu yüzden Node'ların o sırada ne yaptığını gösteren bir kayıt yok.",
  "Last observed: {time}.": "Son gözlem: {time}.",
  "OpsTide was watching what this concerns at this time.": "OpsTide bu sırada bu kaydın ilgili olduğu her şeyi izliyordu.",
  "OpsTide had not yet begun watching what this concerns at this time.":
    "OpsTide bu sırada bu kaydın ilgili olduğu şeyleri izlemeye henüz başlamamıştı.",
  "OpsTide was not watching all of what this concerns at this time; related records may be missing.":
    "OpsTide bu sırada bu kaydın ilgili olduğu her şeyi izlemiyordu; ilgili kayıtlar eksik olabilir.",
  "Secret metadata is not watched (it is off unless asked for), so nothing says what happened to Secrets then.":
    "Secret metadata'sı izlenmiyor (istenmedikçe kapalıdır); bu yüzden Secret'lara o sırada ne olduğunu gösteren bir kayıt yok.",
  "ConfigMaps are not watched (turned off), so nothing says what happened to them then.":
    "ConfigMap'ler izlenmiyor (kapatıldı); bu yüzden onlara o sırada ne olduğunu gösteren bir kayıt yok.",

  // "What did OpsTide know of this resource at this time?" (explorer_detail.go, explorerState)
  "OpsTide cannot say what was being watched for a resource of this kind.": "OpsTide bu türden bir kaynak için neyin izlendiğini söyleyemez.",
  "Not enough recorded evidence to determine this state. The latest record before {time} is a Kubernetes Event, not a state change: {summary}.":
    "Bu durumu belirlemek için yeterli kayıtlı kanıt yok. {time} öncesindeki son kayıt bir durum değişikliği değil, bir Kubernetes Event'i: {summary}.",
  "Not enough recorded evidence to determine this state.": "Bu durumu belirlemek için yeterli kayıtlı kanıt yok.",
  "This is before OpsTide first watched it.": "Bu an, OpsTide'ın onu ilk izlemeye başlamasından önce.",
  "OpsTide recorded this change at exactly {time}: {summary}": "OpsTide bu değişikliği tam olarak {time} anında kaydetti: {summary}",
  "The latest known change before {time} was recorded ({state}) at {at}: {summary}. OpsTide recorded no later change up to {time}.":
    "{time} öncesinde bilinen son değişiklik {at} anında kaydedildi ({state}): {summary}. OpsTide {time} anına kadar daha sonraki bir değişiklik kaydetmedi.",
  "The latest known change before {time} was recorded ({state}) at {at}: {summary}. OpsTide was not watching all of this at {time}, so a later change may not have been recorded.":
    "{time} öncesinde bilinen son değişiklik {at} anında kaydedildi ({state}): {summary}. OpsTide {time} anında bunun tamamını izlemiyordu; bu yüzden daha sonraki bir değişiklik kaydedilmemiş olabilir.",

  // ---- Relationships: how a record is connected (explorer_detail.go, explorer_before.go, evidence.go, collector)
  "Kubernetes reports a Node's name where the UID would be; this Node had exactly that name when the Event happened.":
    "Kubernetes, UID'nin olması gereken yerde Node'un adını bildirir; Event gerçekleştiğinde bu Node tam olarak bu adı taşıyordu.",
  "No Node OpsTide recorded matches this Event by UID, or by exact name while a Node of that name existed (Node observation may be off).":
    "OpsTide'ın kaydettiği hiçbir Node bu Event ile UID üzerinden ya da o adda bir Node varken tam ad üzerinden eşleşmiyor (Node gözlemi kapalı olabilir).",
  "this record is about the workload": "bu kayıt workload'un kendisiyle ilgili",
  "Pod ownerReference chain, as recorded": "kaydedildiği hâliyle Pod ownerReference zinciri",
  "recorded against the same workload": "aynı workload altında kaydedildi",
  "recorded within this release's window": "bu release'in penceresi içinde kaydedildi",
  "this record is about the release": "bu kayıt release'in kendisiyle ilgili",
  "recorded against the same run": "aynı çalışma altında kaydedildi",
  "recorded against the same scheduled operation": "aynı zamanlanmış operasyon altında kaydedildi",
  // OpsTide's own kinds in a relationship's name (ExplorerPanel)
  "kind|Run": "Çalışma",
  "Job ownerReference to CronJob": "Job'dan CronJob'a ownerReference",
  "OpsTide recorded no owner for this Pod": "OpsTide bu Pod için bir sahip kaydetmedi",
  "the Pod's ownerReference names a ReplicaSet, which is this release's, as OpsTide recorded it":
    "OpsTide'ın kaydına göre Pod'un ownerReference'ı bu release'e ait bir ReplicaSet'i gösteriyor",
  "the Pod's controller-revision-hash label names this revision, as recorded":
    "kaydedildiği hâliyle Pod'un controller-revision-hash label'ı bu revision'ı gösteriyor",
  "OpsTide holds no record of this revision (it may have been removed, or never observed).":
    "OpsTide'da bu revision'a ait kayıt yok (silinmiş ya da hiç gözlenmemiş olabilir).",
  "OpsTide holds no record of this (it may have been removed by retention, or never observed).":
    "OpsTide'da bunun kaydı yok (saklama süresi dolduğu için silinmiş ya da hiç gözlenmemiş olabilir).",
  "A Job no CronJob owns has no page of its own.": "Hiçbir CronJob'a ait olmayan bir Job'un kendi sayfası yoktur.",
  "This kind has no page of its own; see the workload.": "Bu türün kendi sayfası yok; workload'a bakın.",
  "a volume, envFrom or env valueFrom": "bir volume, envFrom ya da env valueFrom ile",
  "a volume, envFrom, env valueFrom or an image pull Secret": "bir volume, envFrom, env valueFrom ya da image pull Secret ile",
  "the {kind}'s Pod template names this {ref} ({label}), as OpsTide last saw it":
    "OpsTide'ın son gördüğü hâliyle {kind} Pod template'i bu {ref} kaynağını adlandırıyor ({label})",
  "Named by name, so it applies to whichever {kind} has that name.": "Adıyla anılıyor; bu yüzden o adı taşıyan hangi {kind} olursa ona uygulanır.",
  "A reference does not say the change reached the workload or affected it.":
    "Bir referans, değişikliğin workload'a ulaştığını ya da onu etkilediğini söylemez.",
  "OpsTide first saw this reference at {time}, after this record; whether it existed then is not known.":
    "OpsTide bu referansı ilk kez {time} anında, bu kayıttan sonra gördü; o sırada var olup olmadığı bilinmiyor.",
  "The template stopped naming it (or the workload went) at {time}.": "Template onu {time} anında adlandırmayı bıraktı (ya da workload kaldırıldı).",
  "No Deployment, StatefulSet, DaemonSet or CronJob OpsTide recorded names this {kind} in its Pod template (it may be read another way, or by something OpsTide does not watch).":
    "OpsTide'ın kaydettiği hiçbir Deployment, StatefulSet, DaemonSet ya da CronJob, Pod template'inde bu {kind} kaynağını adlandırmıyor (başka bir yolla ya da OpsTide'ın izlemediği bir şey tarafından okunuyor olabilir).",
  "the resource this record is about": "bu kaydın ilgili olduğu kaynak",
  "the Node this Pod reported running on: the one Node of that name that existed at the record's time":
    "bu Pod'un üzerinde çalıştığını bildirdiği Node: kaydın zamanında o adı taşıyan tek Node",
  "the Pod reported this Node, but OpsTide holds no single Node of that name at the record's time (Node observation may be off)":
    "Pod bu Node'u bildirdi, ancak OpsTide'da kaydın zamanında o adı taşıyan tek bir Node yok (Node gözlemi kapalı olabilir)",
  "the {kind} {name}'s Pod template names this {ref}, as OpsTide saw it; a reference, not a sign the change reached it":
    "OpsTide'ın gördüğü hâliyle {kind} {name} Pod template'i bu {ref} kaynağını adlandırıyor; bu bir referans, değişikliğin ona ulaştığının işareti değil",
  "recorded against the {subject}, but about this {kind}, not the {other} itself":
    "{subject} altında kaydedildi, ancak {other} kaynağının kendisiyle değil, bu {kind} ile ilgili",
  "the Deployment of the release: {note}": "release'in Deployment'ı: {note}",
  "the Event names no UID, and OpsTide does not match by name": "Event bir UID belirtmiyor ve OpsTide ada göre eşleştirme yapmaz",
  "the autoscaler's spec.scaleTargetRef names the Deployment (by name, same namespace)":
    "autoscaler'ın spec.scaleTargetRef alanı Deployment'ı adlandırıyor (ada göre, aynı namespace'te)",
  "spec.scaleTargetRef names the Deployment; resolved by name in the same namespace":
    "spec.scaleTargetRef Deployment'ı adlandırıyor; aynı namespace'te ada göre çözümlendi",
  "OpsTide has not recorded this resource, so nothing it owns or is owned by is known":
    "OpsTide bu kaynağı kaydetmedi; bu yüzden neye sahip olduğu ya da neye ait olduğu bilinmiyor",
  "the Event names this release's ReplicaSet by UID": "Event, bu release'in ReplicaSet'ini UID ile adlandırıyor",
  "the Event names a {kind} owned by this release's ReplicaSet (ownerReference)":
    "Event, bu release'in ReplicaSet'ine ait bir {kind} kaynağını adlandırıyor (ownerReference)",
  "the Event concerns the {kind} of the same Deployment while this release was current; no link to the release itself is asserted":
    "Event, bu release güncelken aynı Deployment'ın {kind} kaynağıyla ilgili; release'in kendisiyle bir bağ öne sürülmüyor",
  "Pod ownerReference to ReplicaSet to Deployment (recorded)": "Pod'dan ReplicaSet'e, oradan Deployment'a ownerReference (kaydedildi)",
  "Pod ownerReference to ReplicaSet to Deployment": "Pod'dan ReplicaSet'e, oradan Deployment'a ownerReference",
  "Pod ownerReference to {kind} (recorded)": "Pod'dan {kind} kaynağına ownerReference (kaydedildi)",
  "Pod ownerReference to Job": "Pod'dan Job'a ownerReference",
  "Pod ownerReference to {kind}": "Pod'dan {kind} kaynağına ownerReference",
  "regarding names the Node (exact name; kubelet reports the name as the UID)":
    "regarding alanı Node'u adlandırıyor (tam ad; kubelet adı UID olarak bildirir)",

  // ConfigMap and Secret write facts (internal/collector/config.go)
  "yes: keys or values differ from OpsTide's previous observation": "evet: anahtarlar ya da değerler OpsTide'ın önceki gözleminden farklı",
  "no: the same keys and values as OpsTide's previous observation": "hayır: anahtarlar ve değerler OpsTide'ın önceki gözlemiyle aynı",
  "not known: OpsTide has no earlier observation of its content to compare": "bilinmiyor: OpsTide'da içeriğini karşılaştıracak daha önceki bir gözlem yok",
  "yes, between OpsTide's previous observation and this one; which of the writes in between changed it is not known":
    "evet, OpsTide'ın önceki gözlemiyle bu gözlem arasında; aradaki yazmalardan hangisinin değiştirdiği bilinmiyor",
  "yes: this manager's fields include the Secret's data; whether this write changed the data is not known":
    "evet: bu manager'ın alanları Secret'ın verisini içeriyor; bu yazmanın veriyi değiştirip değiştirmediği bilinmiyor",
  "no: this manager's fields are metadata only": "hayır: bu manager'ın alanları yalnızca metadata",

  // ---- Explorer timeline: order, notices, empty answers (internal/api/explorer.go, explorer_context.go)
  "Record time, then record id. Record time is when the source says the thing happened (for a Kubernetes Event, its first occurrence), otherwise when OpsTide first observed it. It never changes once recorded, so paging neither skips nor repeats a record.":
    "Önce kayıt zamanı, sonra kayıt kimliği. Kayıt zamanı, kaynağın olayın gerçekleştiğini söylediği andır (bir Kubernetes Event'i için ilk gerçekleşmesi); kaynak bir zaman vermiyorsa OpsTide'ın onu ilk gözlediği andır. Kaydedildikten sonra hiç değişmez; bu yüzden sayfalama bir kaydı ne atlar ne de tekrarlar.",
  "Part of this range is older than the history OpsTide keeps ({days} days); records older than {time} are not kept (an Event that keeps recurring is kept while it does).":
    "Bu aralığın bir kısmı OpsTide'ın tuttuğu geçmişten ({days} gün) daha eski; {time} öncesindeki kayıtlar tutulmaz (tekrarlamaya devam eden bir Event, tekrarladığı sürece tutulur).",
  "Part of this range is before OpsTide first observed this cluster; what happened then is not known.":
    "Bu aralığın bir kısmı, OpsTide'ın bu cluster'ı ilk gözlemesinden önce; o sırada ne olduğu bilinmiyor.",
  "{label} observation is off.": "{label} için gözlem kapalı.",
  "{label} cannot be read right now ({problem}); records of them may be missing.": "{label} şu anda okunamıyor ({problem}); bunlara ait kayıtlar eksik olabilir.",
  "Not watched for part of this range: {labels}. Records from those stretches may be missing; see Coverage by watch.":
    "Bu aralığın bir kısmında izlenmedi: {labels}. O dönemlere ait kayıtlar eksik olabilir; İzlemeye göre kapsam bölümüne bakın.",
  "This range is older than the history OpsTide keeps.": "Bu aralık, OpsTide'ın tuttuğu geçmişten daha eski.",
  "Records are kept for {days} days; nothing before {time} is held, so nothing can be shown.":
    "Kayıtlar {days} gün tutulur; {time} öncesine ait hiçbir şey tutulmadığı için gösterilecek bir şey yok.",
  "OpsTide has not observed this cluster yet.": "OpsTide bu cluster'ı henüz gözlemedi.",
  "OpsTide first observed this cluster at {time}.": "OpsTide bu cluster'ı ilk olarak {time} anında gözledi.",
  "This range is before OpsTide first observed the cluster.": "Bu aralık, OpsTide'ın cluster'ı ilk gözlemesinden önce.",
  "Not enough recorded evidence to determine what happened.": "Ne olduğunu belirlemek için yeterli kayıtlı kanıt yok.",
  "This range is older than the oldest record OpsTide still holds.": "Bu aralık, OpsTide'ın hâlâ tuttuğu en eski kayıttan daha eski.",
  "The oldest record held is from {time}; older ones were removed to stay within the kept history (age or entry limit).":
    "Tutulan en eski kayıt {time} tarihli; daha eskileri tutulan geçmişin sınırları içinde kalmak için silindi (yaş ya da kayıt sınırı).",
  "Part of this range is before OpsTide first observed the cluster, so an empty result there is not an answer.":
    "Bu aralığın bir kısmı OpsTide'ın cluster'ı ilk gözlemesinden önce; bu yüzden oradaki boş sonuç bir yanıt değildir.",
  "Part of this range is older than the history OpsTide keeps, so an empty result there is not an answer.":
    "Bu aralığın bir kısmı OpsTide'ın tuttuğu geçmişten daha eski; bu yüzden oradaki boş sonuç bir yanıt değildir.",
  "Nothing recorded matches in the part of the range OpsTide covers.": "Aralığın OpsTide'ın kapsadığı kısmında eşleşen bir kayıt yok.",
  "Not enough recorded evidence to determine what happened in the rest.": "Geri kalanında ne olduğunu belirlemek için yeterli kayıtlı kanıt yok.",
  "{label} — {state}: {note}": "{label} — {state}: {note}",
  "The source of these records is not being read.": "Bu kayıtların kaynağı okunmuyor.",
  "OpsTide was not watching these for the whole range: {labels}. Records from the gaps may be missing; an empty result here is not proof that nothing happened.":
    "OpsTide bunları aralığın tamamında izlemiyordu: {labels}. Boşluklardaki kayıtlar eksik olabilir; buradaki boş sonuç hiçbir şey olmadığının kanıtı değildir.",
  "No records match, and there are gaps in what OpsTide watched.": "Eşleşen kayıt yok ve OpsTide'ın izlediklerinde boşluklar var.",
  "OpsTide was watching throughout this range, and nothing recorded matches the filters.":
    "OpsTide bu aralık boyunca izliyordu ve kaydedilenlerden hiçbiri filtrelere uymuyor.",
  "Nothing recorded matches the filters. These records are OpsTide's own account of its watching, not a watch of the cluster.":
    "Kaydedilenlerden hiçbiri filtrelere uymuyor. Bu kayıtlar cluster'ın izlenmesi değil, OpsTide'ın kendi izlemesine dair tuttuğu kayıtlardır.",
  "{n} routine record(s) match but are hidden; show routine records to see them.":
    "{n} rutin kayıt eşleşiyor ama gizli; görmek için rutin kayıtları gösterin.",
  "No records match.": "Eşleşen kayıt yok.",
  "Node observation is off. Nodes are not shown as live; the last time they were observed is given.":
    "Node gözlemi kapalı. Node'lar canlı olarak gösterilmez; son gözlendikleri zaman verilir.",
  "Secret metadata is not watched (it is off unless asked for). Nothing says when Secrets were created, written or deleted.":
    "Secret metadata'sı izlenmiyor (istenmedikçe kapalıdır). Secret'ların ne zaman oluşturulduğunu, yazıldığını ya da silindiğini gösteren bir kayıt yok.",
  "ConfigMaps are not watched (turned off). Nothing says when they were created, written or deleted.":
    "ConfigMap'ler izlenmiyor (kapatıldı). Ne zaman oluşturulduklarını, yazıldıklarını ya da silindiklerini gösteren bir kayıt yok.",
  "OpsTide has not watched this yet.": "OpsTide bunu henüz izlemedi.",
  "OpsTide first watched this after the range.": "OpsTide bunu ilk olarak aralıktan sonra izledi.",
  "First watched {time}; nothing before that is covered.": "İlk izleme: {time}; öncesi kapsanmıyor.",
  "inside maintenance window {name}": "{name} bakım penceresi içinde",

  // ---- System (internal/api/system.go)
  "{label} cannot be watched ({problem}): releases from now on may be missing or unassessable.":
    "{label} izlenemiyor ({problem}): bundan sonraki release'ler eksik olabilir ya da değerlendirilemeyebilir.",
  "Kubernetes Events cannot be read ({problem}): evidence from now on may be missing.":
    "Kubernetes Event'leri okunamıyor ({problem}): bundan sonraki kanıtlar eksik olabilir.",
  "HorizontalPodAutoscalers cannot be read ({problem}): scaling context may be missing.":
    "HorizontalPodAutoscaler'lar okunamıyor ({problem}): ölçekleme bağlamı eksik olabilir.",
  "{label} cannot be watched ({problem}): what is shown for them may be missing.": "{label} izlenemiyor ({problem}): bunlar için gösterilenler eksik olabilir.",
  "{label} cannot be watched ({problem}): changes from now on may be missing.": "{label} izlenemiyor ({problem}): bundan sonraki değişiklikler eksik olabilir.",
  "Secret metadata": "Secret metadata'sı",
  "ControllerRevisions (revision history)": "ControllerRevision'lar (revision geçmişi)",
  "Off. Nodes are cluster-scoped: observing them needs list and watch on nodes cluster-wide (nodes.observe).":
    "Kapalı. Node'lar cluster kapsamlıdır: onları gözlemek için cluster genelinde nodes üzerinde list ve watch yetkisi gerekir (nodes.observe).",
  "Node conditions and spec.unschedulable are recorded; capacity, labels, taints and images are not read.":
    "Node condition'ları ve spec.unschedulable kaydedilir; kapasite, label'lar, taint'ler ve image'lar okunmaz.",
  "Created, written (by which field manager) and deleted are recorded, with the names of keys and a keyed hash (HMAC, under a key made for this install) of the content; values are dropped as they arrive and never kept.":
    "Oluşturulma, yazılma (hangi field manager tarafından) ve silinme; anahtar adları ve içeriğin anahtarlı bir hash'i (bu kurulum için üretilmiş bir anahtarla HMAC) ile birlikte kaydedilir; değerler geldikleri anda atılır ve asla tutulmaz.",
  "Off (watch.configMaps). Nothing says when ConfigMaps were created, written or deleted.":
    "Kapalı (watch.configMaps). ConfigMap'lerin ne zaman oluşturulduğunu, yazıldığını ya da silindiğini gösteren bir kayıt yok.",
  "Metadata only: Secret data is never requested. Annotations such as kubectl's last-applied-configuration can contain it, and managedFields name its keys: both arrive with the metadata and are dropped the moment they arrive, never cached or kept. Key names of Secrets are never kept, and a write never says the data changed.":
    "Yalnızca metadata: Secret verisi asla istenmez. kubectl'in last-applied-configuration gibi annotation'ları bu veriyi içerebilir, managedFields ise anahtarlarını adlandırır: ikisi de metadata ile gelir ve geldikleri anda atılır; asla önbelleğe alınmaz ya da tutulmaz. Secret'ların anahtar adları asla tutulmaz ve bir yazma, verinin değiştiğini asla söylemez.",
  "Off (watch.secretMetadata). Secrets are not read at all.": "Kapalı (watch.secretMetadata). Secret'lar hiç okunmaz.",
  "Open an object in Clustrail from any entry.": "Herhangi bir kayıttan bir nesneyi Clustrail'de açın.",

  // ---- Workloads (internal/api/workloads.go, internal/workload/rules.go)
  "{label} cannot be read ({problem}): what is shown may be incomplete.": "{label} okunamıyor ({problem}): gösterilenler eksik olabilir.",
  "Kubernetes' status for this workload has not been recorded yet.": "Bu workload için Kubernetes'in bildirdiği durum henüz kaydedilmedi.",
  "Comparison unavailable: the stored snapshot cannot be read.": "Karşılaştırma yapılamıyor: saklanan snapshot okunamıyor.",
  "Comparison unavailable: no earlier revision of this Deployment is recorded.": "Karşılaştırma yapılamıyor: bu Deployment'ın daha önceki bir revision'ı kayıtlı değil.",
  "Comparison unavailable: the earlier snapshot cannot be read.": "Karşılaştırma yapılamıyor: önceki snapshot okunamıyor.",
  "Comparison unavailable: the ControllerRevision's Pod template could not be read.": "Karşılaştırma yapılamıyor: ControllerRevision'ın Pod template'i okunamadı.",
  "Comparison unavailable: no earlier revision is recorded (Kubernetes keeps a limited history, and OpsTide records only what it saw).":
    "Karşılaştırma yapılamıyor: daha önceki bir revision kayıtlı değil (Kubernetes sınırlı bir geçmiş tutar, OpsTide da yalnızca gördüğünü kaydeder).",
  "Comparison unavailable: the earlier ControllerRevision's Pod template could not be read.":
    "Karşılaştırma yapılamıyor: önceki ControllerRevision'ın Pod template'i okunamadı.",
  "A Deployment's revisions are its releases (ReplicaSets); the release page has the rollout, health and evidence of each.":
    "Bir Deployment'ın revision'ları onun release'leridir (ReplicaSet'ler); her birinin rollout'u, sağlığı ve kanıtları release sayfasındadır.",
  "The rolling update stopped at its partition boundary: Pods below the partition stay on the previous revision by design.":
    "Rolling update partition sınırında durdu: partition'ın altındaki Pod'lar tasarım gereği önceki revision'da kalır.",
  "The update strategy is OnDelete: a changed template replaces no Pod until it is deleted, so the update revision is not yet what is running.":
    "Update stratejisi OnDelete: değişen bir template, Pod silinene kadar hiçbir Pod'un yerine geçmez; bu yüzden update revision henüz çalışan revision değil.",
  "A DaemonSet's status carries no revision: the newest ControllerRevision is shown as its template revision.":
    "Bir DaemonSet'in status'u revision taşımaz: en yeni ControllerRevision, template revision'ı olarak gösterilir.",
  "Nodes are not observed, so which Nodes have no Pod of this DaemonSet cannot be listed. Turn on Node observation (nodes.observe) to see it.":
    "Node'lar gözlenmiyor; bu yüzden hangi Node'larda bu DaemonSet'in Pod'u olmadığı listelenemez. Görmek için Node gözlemini açın (nodes.observe).",
  "No Node is recorded (the Node watch is failing or has not listed yet), so which Nodes have no Pod of this DaemonSet is unknown.":
    "Kayıtlı Node yok (Node izlemesi başarısız oluyor ya da henüz listelemedi); bu yüzden hangi Node'larda bu DaemonSet'in Pod'u olmadığı bilinmiyor.",
  "No Pod of this DaemonSet is recorded on these Nodes. Whether they are eligible for it (node selector, taints, tolerations, affinity) is not established by OpsTide, so no explanation is given.":
    "Bu Node'larda bu DaemonSet'e ait kayıtlı Pod yok. Node'ların ona uygun olup olmadığı (node selector, taint'ler, toleration'lar, affinity) OpsTide tarafından belirlenmez; bu yüzden bir açıklama verilmez.",
  "Reported by Kubernetes: {desired} desired, {current} currently scheduled, {misscheduled} misscheduled, {available} available.":
    "Kubernetes'in bildirdiği: {desired} istenen, {current} şu an zamanlanmış, {misscheduled} yanlış zamanlanmış, {available} erişilebilir.",
  "Kubernetes has not yet reported status for generation {generation} (observed generation {observed}).":
    "Kubernetes, generation {generation} için henüz status bildirmedi (observed generation {observed}).",
  "The current revision equals the update revision.": "Current revision, update revision ile aynı.",
  "The update strategy is OnDelete: all {n} of {m} Pods are on the update revision (Kubernetes does not advance currentRevision under OnDelete).":
    "Update stratejisi OnDelete: {m} Pod'un tamamı ({n}) update revision'da (Kubernetes OnDelete altında currentRevision'ı ilerletmez).",
  "The update strategy is OnDelete: a Pod is replaced only when it is deleted. {n} of {m} scheduled Pods run the update revision.":
    "Update stratejisi OnDelete: bir Pod yalnızca silindiğinde yenisiyle değiştirilir. Zamanlanmış {m} Pod'dan {n} tanesi update revision'ı çalıştırıyor.",
  "The update strategy is OnDelete: a Pod is replaced only when it is deleted. {n} of {m} Pods are on the update revision.":
    "Update stratejisi OnDelete: bir Pod yalnızca silindiğinde yenisiyle değiştirilir. {m} Pod'dan {n} tanesi update revision'da.",
  "The rolling update reached its partition boundary (partition {p}): {n} of {m} Pods are on the update revision, and Pods with a lower ordinal stay on the previous revision by design.":
    "Rolling update partition sınırına ulaştı (partition {p}): {m} Pod'dan {n} tanesi update revision'da; daha düşük sıra numaralı Pod'lar tasarım gereği önceki revision'da kalır.",
  "The update revision differs from the current revision: {n} of {m} Pods are on the update revision.":
    "Update revision, current revision'dan farklı: {m} Pod'dan {n} tanesi update revision'da.",
  "{n} of {m} scheduled Pods run the update revision.": "Zamanlanmış {m} Pod'dan {n} tanesi update revision'ı çalıştırıyor.",
  // the demo's StatefulSet (internal/demo/demo.go)
  "updated {n} of {m} replicas; partition {p} holds the lower ordinals on the previous revision":
    "{m} replica'dan {n} tanesi güncellendi; partition {p}, düşük sıra numaralı Pod'ları önceki revision'da tutuyor",

  // ---- Nodes and runs (internal/api/nodes.go)
  "Node observation is off. It needs list and watch on nodes cluster-wide; turn it on with nodes.observe (OPSTIDE_OBSERVE_NODES).":
    "Node gözlemi kapalı. Cluster genelinde nodes üzerinde list ve watch yetkisi gerekir; nodes.observe (OPSTIDE_OBSERVE_NODES) ile açın.",
  "No Pod OpsTide observed has reported running on this Node. Pods it did not observe are not listed, and Job Pods are not placed here.":
    "OpsTide'ın gözlediği hiçbir Pod bu Node üzerinde çalıştığını bildirmedi. Gözlemediği Pod'lar listelenmez ve Job Pod'ları buraya yerleştirilmez.",
  "Events Kubernetes had already dropped when OpsTide began watching are not recorded, and an Event is linked to this run only through the UID of its Job or the ownerReference of its Pod.":
    "OpsTide izlemeye başladığında Kubernetes'in çoktan sildiği Event'ler kaydedilmez; bir Event bu çalışmaya yalnızca Job'unun UID'si ya da Pod'unun ownerReference'ı üzerinden bağlanır.",
  "No Kubernetes Event is recorded for this run.": "Bu çalışma için kayıtlı Kubernetes Event'i yok.",
  "More Events are recorded than are shown.": "Gösterilenden daha fazla Event kayıtlı.",
  "OpsTide cannot read Kubernetes Events right now: evidence from now on may be missing.":
    "OpsTide şu anda Kubernetes Event'lerini okuyamıyor: bundan sonraki kanıtlar eksik olabilir.",

  // ---- Releases (internal/api/releases.go, evidence.go; internal/release; internal/evidence)
  "{n} more gaps in this range; see System.": "Bu aralıkta {n} boşluk daha var; System sayfasına bakın.",
  "OpsTide was not watching {label} from {from} to {to} UTC; releases created then are marked adopted and cannot be assessed.":
    "OpsTide {from}–{to} UTC arasında {label} izlemiyordu; o sırada oluşturulan release'ler devralındı olarak işaretlenir ve değerlendirilemez.",
  "The preceding revision was not observed by OpsTide and no snapshot of it exists.": "Önceki revision OpsTide tarafından gözlenmedi ve bir snapshot'ı yok.",
  "OpsTide does not reconstruct it from the current Deployment.": "OpsTide onu mevcut Deployment'tan yeniden oluşturmaz.",
  "The preceding revision's record is no longer kept (retention).": "Önceki revision'ın kaydı artık tutulmuyor (saklama süresi).",
  "A stored snapshot cannot be read.": "Saklanan bir snapshot okunamıyor.",
  "The preceding revision existed before OpsTide started: its Pod template is the one Kubernetes kept for it, but its rollout was not observed.":
    "Önceki revision OpsTide başlamadan önce de vardı: Pod template'i Kubernetes'in onun için tuttuğu template'tir, ancak rollout'u gözlenmedi.",
  "Replica count and rollout strategy were not compared: they are known only for revisions OpsTide saw roll out.":
    "Replica sayısı ve rollout stratejisi karşılaştırılmadı: bunlar yalnızca OpsTide'ın rollout'unu gördüğü revision'lar için bilinir.",
  "This release existed before OpsTide began watching: Events from before then are recorded only if Kubernetes still held them at that time.":
    "Bu release, OpsTide izlemeye başlamadan önce de vardı: o andan önceki Event'ler yalnızca Kubernetes o sırada onları hâlâ tutuyorsa kaydedildi.",
  "{n} of {m} replicas updated, {available} available": "{m} replica'dan {n} tanesi güncellendi, {available} tanesi erişilebilir",
  "the Deployment controller has not yet seen the latest spec": "Deployment controller en son spec'i henüz görmedi",
  "replaced by a newer revision": "daha yeni bir revision onun yerini aldı",
  "the Deployment was deleted": "Deployment silindi",

  // recorded evidence summary (internal/evidence/summary.go)
  "Not established by OpsTide.": "OpsTide tarafından belirlenmedi.",
  "Revision {revision} was already present when OpsTide began watching; its rollout was not observed.":
    "Revision {revision}, OpsTide izlemeye başladığında zaten vardı; rollout'u gözlenmedi.",
  "Revision {revision} was observed at {time}.": "Revision {revision}, {time} anında gözlendi.",
  "{n} Pod was observed under ReplicaSet {name}.": "ReplicaSet {name} altında {n} Pod gözlendi.",
  "{n} Pods were observed under ReplicaSet {name}.": "ReplicaSet {name} altında {n} Pod gözlendi.",
  "Kubernetes reported the rollout {state} at {time}: {reason}.": "Kubernetes rollout'u {time} anında {state} olarak bildirdi: {reason}.",
  "Kubernetes reported {reason} for {kind} {name} at {time} (count reported by Kubernetes: {n}).":
    "Kubernetes, {time} anında {kind} {name} için {reason} bildirdi (Kubernetes'in bildirdiği sayı: {n}).",
  "Kubernetes reported {reason} for {kind} {name} at {time} ({n} separate Events are recorded).":
    "Kubernetes, {time} anında {kind} {name} için {reason} bildirdi ({n} ayrı Event kayıtlı).",
  "Kubernetes reported {reason} for {kind} {name} (count reported by Kubernetes: {n}).":
    "Kubernetes, {kind} {name} için {reason} bildirdi (Kubernetes'in bildirdiği sayı: {n}).",
  "Kubernetes reported {reason} for {kind} {name} ({n} separate Events are recorded).":
    "Kubernetes, {kind} {name} için {reason} bildirdi ({n} ayrı Event kayıtlı).",
  "Kubernetes reported {reason} for {kind} {name} at {time}.": "Kubernetes, {time} anında {kind} {name} için {reason} bildirdi.",
  "Kubernetes reported {reason} for {kind} {name}.": "Kubernetes, {kind} {name} için {reason} bildirdi.",
  "{n} more Warning reason are recorded; see the records below.": "{n} Warning reason daha kayıtlı; aşağıdaki kayıtlara bakın.",
  "{n} more Warning reasons are recorded; see the records below.": "{n} Warning reason daha kayıtlı; aşağıdaki kayıtlara bakın.",
  "HPA {hpa} reported desired replicas changed from {from} to {to} (observed at {time}).":
    "HPA {hpa}, istenen replica sayısının {from} değerinden {to} değerine değiştiğini bildirdi ({time} anında gözlendi).",
  "The Deployment's replica target changed from {from} to {to} (observed at {time}).":
    "Deployment'ın replica hedefi {from} değerinden {to} değerine değişti ({time} anında gözlendi).",
  "The records this list is built from were cut off at the limit, so it may be missing facts.":
    "Bu listenin oluşturulduğu kayıtlar sınırda kesildi; bu yüzden listede eksik bilgi olabilir.",

  // release assessment: reasons, coverage limits and findings (internal/release/assess.go)
  "The release existed before OpsTide started watching, so its rollout and its first minutes were not observed.":
    "Release, OpsTide izlemeye başlamadan önce de vardı; bu yüzden rollout'u ve ilk dakikaları gözlenmedi.",
  "OpsTide was not watching Deployments, ReplicaSets and Pods without a gap during the observation window.":
    "OpsTide gözlem penceresi boyunca Deployment, ReplicaSet ve Pod'ları kesintisiz izlemedi.",
  "Observation coverage could not be checked.": "Gözlem kapsamı denetlenemedi.",
  "No Pod of this release was ever observed, so nothing can be said about its Pods.":
    "Bu release'in hiçbir Pod'u gözlenmedi; bu yüzden Pod'ları hakkında bir şey söylenemez.",
  "A newer revision replaced it before its rollout completed, so no post-deployment window followed.":
    "Rollout'u tamamlanmadan daha yeni bir revision onun yerini aldı; bu yüzden ardından dağıtım sonrası bir pencere gelmedi.",
  "A newer revision replaced it before its observation window ended.": "Gözlem penceresi bitmeden daha yeni bir revision onun yerini aldı.",
  "Evidence is also incomplete: {note}": "Kanıtlar da eksik: {note}",
  "The rollout has not completed; the observation window starts when it does.": "Rollout tamamlanmadı; gözlem penceresi rollout tamamlandığında başlar.",
  "Observing until {time}; nothing against the release so far.": "{time} saatine kadar gözleniyor; şu ana kadar release aleyhine bir şey yok.",
  "Evidence is already incomplete, which will make the verdict insufficient unless something is found: {note}":
    "Kanıtlar şimdiden eksik; bir şey bulunmazsa sonuç bu yüzden yetersiz olacak: {note}",
  "The rollout completed and {n} minutes after it passed with no restart, OOMKill, waiting reason, NotReady transition or lasting loss of availability, and OpsTide watched throughout.":
    "Rollout tamamlandı ve ardından geçen {n} dakikada hiçbir yeniden başlatma, OOMKill, bekleme nedeni, NotReady geçişi ya da kalıcı erişilebilirlik kaybı olmadı; OpsTide bu süre boyunca izledi.",
  "The rollout completed and {n} minutes after it passed with no restart, OOMKill, waiting reason, NotReady transition or loss of availability below the earlier target, and OpsTide watched throughout.":
    "Rollout tamamlandı ve ardından geçen {n} dakikada hiçbir yeniden başlatma, OOMKill, bekleme nedeni, NotReady geçişi ya da önceki hedefin altına düşen erişilebilirlik kaybı olmadı; OpsTide bu süre boyunca izledi.",
  "Not counted against the release ({rule}): {note}.": "Release aleyhine sayılmadı ({rule}): {note}.",
  "Desired replicas changed from {from} to {to}; available replicas stayed at or above {floor} (lowest observed: {lowest}) while the new target was being reached, and it had not been reached yet":
    "İstenen replica sayısı {from} değerinden {to} değerine değişti; yeni hedefe ulaşılırken erişilebilir replica sayısı {floor} veya üzerinde kaldı (gözlenen en düşük: {lowest}) ve hedefe henüz ulaşılmamıştı",
  "Desired replicas changed from {from} to {to}; available replicas stayed at or above {floor} (lowest observed: {lowest}) while the new target was being reached":
    "İstenen replica sayısı {from} değerinden {to} değerine değişti; yeni hedefe ulaşılırken erişilebilir replica sayısı {floor} veya üzerinde kaldı (gözlenen en düşük: {lowest})",
  "The rollout was {state}: {reason}": "Rollout {state} durumuna geçti: {reason}",
  "The rollout is {state}: {reason}": "Rollout {state} durumunda: {reason}",
  "Container {container} in pod {pod} was OOMKilled": "{pod} pod'undaki {container} container'ı OOMKilled oldu",
  "Container {container} in pod {pod} restarted ({reason}), {n} times since last seen":
    "{pod} pod'undaki {container} container'ı yeniden başladı ({reason}), son görülmesinden bu yana {n} kez",
  "Container {container} in pod {pod} restarted ({reason})": "{pod} pod'undaki {container} container'ı yeniden başladı ({reason})",
  "Container {container} in pod {pod} restarted, {n} times since last seen": "{pod} pod'undaki {container} container'ı yeniden başladı, son görülmesinden bu yana {n} kez",
  "Container {container} in pod {pod} restarted": "{pod} pod'undaki {container} container'ı yeniden başladı",
  "Container {container} in pod {pod} is waiting: {reason}": "{pod} pod'undaki {container} container'ı bekliyor: {reason}",
  "Pod {pod} went from Ready to NotReady": "Pod {pod}, Ready durumundan NotReady durumuna geçti",
  "Pod {pod} was evicted": "Pod {pod} tahliye edildi (evicted)",
  "Pod {pod} ended in phase Failed": "Pod {pod}, Failed aşamasında sona erdi",
  "Available replicas fell from {a} to {b} while the target was raised from {from} to {to}":
    "Hedef {from} değerinden {to} değerine yükseltilirken erişilebilir replica sayısı {a} değerinden {b} değerine düştü",
  "Available replicas fell to {a}, below the earlier target of {b}, while the target was raised from {from} to {to}":
    "Hedef {from} değerinden {to} değerine yükseltilirken erişilebilir replica sayısı {a} değerine, önceki {b} hedefinin altına düştü",
  "Fewer replicas were available than wanted for {d} and still are": "{d} boyunca istenenden daha az replica erişilebilir durumdaydı ve hâlâ öyle",
  "Fewer replicas were available than wanted for {d}": "{d} boyunca istenenden daha az replica erişilebilir durumdaydı",
  // the demo's seeded health reasons (internal/demo/demo.go)
  "No restarts and no unavailable Pods in the observation window": "Gözlem penceresinde yeniden başlatma ve erişilemeyen Pod yok",
  "{n} Pod restarts in the window (OOMKilled twice)": "Pencerede {n} Pod yeniden başlatması (iki kez OOMKilled)",
  "{n} Pod was not ready for {m} minutes": "{n} Pod {m} dakika boyunca hazır değildi",
}
