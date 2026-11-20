"""Writes website/index.html and website/tr/index.html from one template: python3 website/scripts/build.py

Both pages are made from the dictionaries below, EN and TR, so they stay alike. Edit here, never the HTML.
"""
import pathlib, html

ROOT = pathlib.Path(__file__).resolve().parent.parent
GH = 'https://github.com/mihnk/opstide'
HELM = 'helm install opstide oci://ghcr.io/mihnk/charts/opstide -n opstide --create-namespace'

ICON = {
    'mark': '<svg width="28" height="28" viewBox="0 0 32 32" fill="none" aria-hidden="true" focusable="false"><path d="M3 20c4 0 4-4 8.5-4S16 20 20.5 20 25 16 29 16" stroke="#4cc9a0" stroke-width="2.6" stroke-linecap="round"/><path d="M3 26c4 0 4-4 8.5-4S16 26 20.5 26 25 22 29 22" stroke="#6fb7ff" stroke-width="2.6" stroke-linecap="round" opacity=".7"/><path d="M16 4v9" stroke="#e8e6e1" stroke-width="2.6" stroke-linecap="round"/><circle cx="16" cy="4" r="2.4" fill="#e8e6e1"/></svg>',
    'github': '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M9 19c-4.3 1.4-4.3-2.5-6-3m12 5v-3.5c0-1 .1-1.4-.5-2 2.8-.3 5.5-1.4 5.5-6a4.6 4.6 0 0 0-1.3-3.2 4.2 4.2 0 0 0-.1-3.2s-1.1-.3-3.5 1.3a12.3 12.3 0 0 0-6.2 0C6.5 2.8 5.4 3.1 5.4 3.1a4.2 4.2 0 0 0-.1 3.2A4.6 4.6 0 0 0 4 9.5c0 4.6 2.7 5.7 5.5 6-.6.6-.6 1.2-.5 2V21"/></svg>',
    'menu': '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true" focusable="false"><path d="M4 7h16M4 12h16M4 17h16"/></svg>',
    'copy': '<svg class="icon-copy" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true" focusable="false"><rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg><svg class="icon-done" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" aria-hidden="true" focusable="false"><path d="M5 12l5 5L20 7"/></svg>',
    'arrow': '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" aria-hidden="true" focusable="false"><path d="M5 12h14M13 6l6 6-6 6"/></svg>',
    'eye': '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="#17222d" stroke-width="2" stroke-linecap="round" aria-hidden="true" focusable="false"><path d="M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/></svg>',
    'lock': '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="#17222d" stroke-width="2" stroke-linecap="round" aria-hidden="true" focusable="false"><rect x="4" y="10" width="16" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3"/></svg>',
    'cube': '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="#17222d" stroke-width="2" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z"/></svg>',
    'bars': '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="#17222d" stroke-width="2" stroke-linecap="round" aria-hidden="true" focusable="false"><path d="M4 19h16M7 15V9M12 15V5M17 15v-4"/></svg>',
    'check': '<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="#1e8e6a" stroke-width="2.4" stroke-linecap="round" aria-hidden="true" focusable="false"><path d="M5 12l5 5L20 7"/></svg>',
    'pen': '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#1a62b8" stroke-width="2" stroke-linecap="round" aria-hidden="true" focusable="false"><path d="M4 20h4L19 9l-4-4L4 16z"/><path d="M13 7l4 4"/></svg>',
    'doc': '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#1a62b8" stroke-width="2" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M6 3h8l4 4v14H6z"/><path d="M14 3v4h4M9 12h6M9 16h6"/></svg>',
    'box3d': '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#1a62b8" stroke-width="2" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z"/><path d="M12 12l8-4.5M12 12v9M12 12L4 7.5"/></svg>',
    'clock': '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#1a62b8" stroke-width="2" stroke-linecap="round" aria-hidden="true" focusable="false"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg>',
    'history': '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#1a62b8" stroke-width="2" stroke-linecap="round" aria-hidden="true" focusable="false"><path d="M3 12a9 9 0 1 0 3-6.7"/><path d="M3 4v4h4M12 8v4l3 2"/></svg>',
    'chat': '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#1a62b8" stroke-width="2" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M4 5h16v11h-9l-5 4v-4H4z"/></svg>',
    'cal': '<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="#1a62b8" stroke-width="2" stroke-linecap="round" aria-hidden="true" focusable="false"><rect x="3" y="5" width="18" height="16" rx="2"/><path d="M3 10h18M8 3v4M16 3v4"/></svg>',
    'flow': '<svg width="40" height="20" viewBox="0 0 40 20" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true" focusable="false"><path d="M2 10h34M28 3l8 7-8 7"/></svg>',
}

EN = dict(
    lang='en', other='tr', locale='en_US', other_locale='tr_TR', home='/', prefix='',
    title='OpsTide — the history of your Kubernetes cluster',
    description='OpsTide is an open-source, read-only recorder for Kubernetes: what changed, what ran and what failed, with the stretches it was not watching marked. One pod, SQLite; nothing leaves the cluster unless you ask for a morning summary.',
    copied='Copied', copy_failed='Copy failed',
    skip='Skip to content', home_label='OpsTide home', site_nav='Site', menu='Menu', lang_nav='Language',
    nav=[('how', 'How it works'), ('features', 'Features'), ('integrations', 'Integrations'), ('security', 'Security'), ('install', 'Install')],
    intro='Introduction',
    pill='Open source · Kubernetes · read-only',
    h1='Know what happened<br> in your cluster.',
    lead='OpsTide records what changed, what ran and what failed — and marks the stretches it wasn’t watching, so a quiet hour never reads as a calm one.',
    cmd_label='Install command', copy_cmd='Copy install command',
    cta='Try the demo — no cluster needed',
    hero_alt='The OpsTide Explorer in the dark theme: a histogram of the last 24 hours, filters by record type, the list of records, and the panel of a Pod restarted after an OOMKill with its Before tab open, listing the Deployment spec write and the ConfigMap write recorded in the hour before it.',
    glance='At a glance',
    facts=[('eye', 'list &amp; watch only', 'Never writes to the cluster'), ('lock', 'Nothing leaves by default', 'Only a morning summary, if you set one up'),
           ('cube', 'One pod, one file', 'A single binary with SQLite'), ('bars', 'Apache 2.0', 'Open source, no account')],
    p_eyebrow='Evidence, not verdicts',
    p_h2='It writes down what happened. Deciding why stays with you.',
    p=[('OBSERVED', 'Every record says how it is known', 'Observed from the API, derived by a documented rule, or unknown. Nothing is dressed up as more certain than it is.'),
       ('', 'Gaps are drawn, not hidden', 'When it wasn’t watching — a restart, a missing permission — that time is hatched. A quiet stretch is never mistaken for a calm one.'),
       ('connection, not cause', 'It links, it never blames', 'It shows what changed before a failure — on the workload, its config, its node — and leaves the conclusion to you or your agent.')],
    ov_num='01 · Overview', ov_h2='Your day as a timetable.',
    ov_p='Every CronJob run on one line, failures in red, what’s still due outlined. A five-day strip to scroll through time, the exceptions that need a look, and planned maintenance as a band — not as an alarm.',
    ov_checks=['Missed runs and runs that ran long, grouped', 'Run time against its own baseline: “+34% longer”', 'Your time zone or UTC, in English or Turkish'],
    ov_alt='The Overview in the light theme: counts for the last 24 hours, the watches that were not whole, a five-day strip of runs per hour with failures in red, and today’s timetable of CronJob runs.',
    ex_num='02 · Explorer', ex_h2='Every record, one search away.',
    ex_p='Filter like you would in your log tool — <code class="chip chip--ns">namespace:shop</code> <code class="chip chip--sev">severity:warning</code> — then open a failure and see what was recorded in the hour before it.',
    before='Recorded before this',
    before_items=['<span class="ago">7 min</span> · Deployment spec written by <b>kubectl-edit</b>',
                  '<span class="ago">12 min</span> · ConfigMap it references changed, by <b>argocd-controller</b>'],
    ex_alt='The Explorer in the light theme with two filters applied as tokens in the search bar, namespace shop and severity warning, a histogram of the last 24 hours and the matching records: a Pod restarted after an OOMKill, a Warning Event and a stretch that was not watched.',
    mo_num='03 · Morning summary', mo_h2='The night, in your channel before the day starts.',
    mo_p='At 08:30, or whenever you set, OpsTide posts what the night looked like to Slack, Microsoft Teams or a webhook of yours: failed runs, CronJobs overdue for a success, restarts with the change recorded just before them, and the stretches it was not watching. A quiet night says so.',
    mo_checks=['Set up in two steps from the UI, which writes the Secret command and the chart values', 'The webhook URL stays in a Kubernetes Secret: never shown, never logged', 'Sent late if OpsTide was down at its time, never twice'],
    mo_alt='The morning summary setup in the light theme: Slack, Microsoft Teams or a webhook to choose from, the time, how far back it looks and what it includes, beside a preview of the message built from the cluster’s record, with two failed runs, an overdue CronJob and a Pod restarted after an OOMKill.',
    keeps_h2='What it keeps for you',
    keeps=[('pen', 'Who changed it', 'Every spec write with the client that made it — argocd, helm, kubectl — from Kubernetes’ own field managers.'),
           ('doc', 'Config changes, no values', 'ConfigMap writes and the keys that changed, never their values. Secrets: metadata only, and only if you turn it on.'),
           ('box3d', 'Releases and revisions', 'Each rollout with its Pods, what changed in the template, and a fixed rule’s findings — not a health score.'),
           ('clock', 'Any moment, again', 'Which image ran where last Tuesday at 10:00 — and whether OpsTide was watching then.'),
           ('history', 'Scheduled jobs, kept honest', 'Missed starts, run time against a 23-day baseline, and CronJobs overdue for a success — said only when it was watching.'),
           ('cal', 'Planned work, labelled', 'Maintenance windows from your Helm values mark what happened inside them. Nothing is muted.')],
    fits_eyebrow='How it fits', fits_h2='Next to your stack, not in its way.',
    core_eyebrow='OpsTide · one pod',
    core_p='Watches with list &amp; watch, writes a timeline to SQLite on its own volume, serves the UI and a read API. Keeps 30 days by default.',
    core_mono='token auth · /healthz · /readyz',
    outputs=[('Your channel', 'a morning summary on Slack, Teams or a webhook, if you want one'), ('Prometheus', 'overdue CronJobs and its own health, for alerts'), ('Grafana', 'changes &amp; failures as annotations'), ('Your agent', 'versioned JSON evidence export')],
    sec_eyebrow='Security', sec_h2='Built to be allowed in production.',
    sec_p='<a href="' + GH + '/blob/main/docs/install.md">Read the full RBAC</a> before you install. It is short.',
    rbac=[('Verbs', '<span class="mono">list · watch</span>'),
          ('Never asked for', 'exec, logs, Secret data, any write'),
          ('Secrets', 'Off by default. When on: metadata-only requests, with the last-applied annotation and managedFields dropped on arrival. The grant itself is list &amp; watch on secrets.'),
          ('Scope', 'Whole cluster, or only the namespaces you list'),
          ('Access', 'A token for the UI and API, or sign-in by name through oauth2-proxy'),
          ('Outbound', 'Nothing, unless you set up the morning summary: then HTTPS to the destinations you list')],
    inst_h2='Running in two minutes',
    demo_eyebrow='Try it first · no cluster, no Docker',
    demo_comment='# sample data, opens on 127.0.0.1:8080',
    demo_note='Needs Go 1.26, and Node 22 to build the UI.',
    copy_demo='Copy demo commands',
    helm_eyebrow='Install in your cluster',
    helm_comment='# then open http://127.0.0.1:8080',
    helm_note='Sign in with the token from the <code>opstide-auth</code> Secret; every chart value is in the <a href="' + GH + '/blob/main/docs/install.md">install guide</a>. OpsTide is in early development (v0.1): the chart is published with the first release.',
    copy_helm='Copy install commands',
    foot_tag='Records evidence. Agents interpret evidence.',
    foot_links=[('GitHub', GH), ('Documentation', GH + '/tree/main/docs'), ('Releases', GH + '/releases')],
    license='Apache 2.0',
)

TR = dict(
    lang='tr', other='en', locale='tr_TR', other_locale='en_US', home='/tr/', prefix='-tr',
    title='OpsTide — Kubernetes cluster’ınızın geçmişi',
    description='OpsTide, Kubernetes için açık kaynaklı, salt okunur bir kayıt aracıdır: neyin değiştiğini, neyin çalıştığını ve neyin başarısız olduğunu kaydeder, izlemediği aralıkları da işaretler. Tek pod, SQLite; siz bir sabah özeti istemedikçe hiçbir şey cluster’dan çıkmaz.',
    copied='Kopyalandı', copy_failed='Kopyalanamadı',
    skip='İçeriğe geç', home_label='OpsTide ana sayfa', site_nav='Site', menu='Menü', lang_nav='Dil',
    nav=[('how', 'Nasıl çalışır'), ('features', 'Özellikler'), ('integrations', 'Entegrasyonlar'), ('security', 'Güvenlik'), ('install', 'Kurulum')],
    intro='Giriş',
    pill='Açık kaynak · Kubernetes · salt okunur',
    h1='Cluster’ınızda<br> ne olduğunu bilin.',
    lead='OpsTide neyin değiştiğini, neyin çalıştığını ve neyin başarısız olduğunu kaydeder; izlemediği aralıkları da işaretler. Böylece sessiz bir saat asla sakin bir saat gibi okunmaz.',
    cmd_label='Kurulum komutu', copy_cmd='Kurulum komutunu kopyala',
    cta='Demoyu deneyin — cluster gerekmez',
    hero_alt='Koyu temada OpsTide Explorer: son 24 saatin histogramı, kayıt türüne göre filtreler, kayıt listesi ve OOMKill sonrası yeniden başlayan bir Pod’un paneli. Öncesinde sekmesi açık; ondan önceki bir saatte kaydedilen Deployment spec yazımını ve ConfigMap yazımını listeliyor.',
    glance='Bir bakışta',
    facts=[('eye', 'Yalnızca list ve watch', 'Cluster’a asla yazmaz'), ('lock', 'Varsayılan olarak hiçbir şey çıkmaz', 'Yalnızca kurarsanız bir sabah özeti'),
           ('cube', 'Tek pod, tek dosya', 'SQLite ile tek bir binary'), ('bars', 'Apache 2.0', 'Açık kaynak, hesap gerekmez')],
    p_eyebrow='Hüküm değil, kanıt',
    p_h2='Ne olduğunu yazar. Nedenine karar vermek size kalır.',
    p=[('GÖZLENDİ', 'Her kayıt nasıl bilindiğini söyler', 'API’den gözlendi, belgelenmiş bir kuralla türetildi ya da bilinmiyor. Hiçbir şey olduğundan daha kesinmiş gibi gösterilmez.'),
       ('', 'Boşluklar gizlenmez, çizilir', 'İzlemediği zamanlar (bir yeniden başlatma, eksik bir yetki) taralı gösterilir. Sessiz bir aralık asla sakin bir aralık sanılmaz.'),
       ('bağlantı, neden değil', 'Bağlar, asla suçlamaz', 'Bir hatadan önce neyin değiştiğini gösterir (workload’da, yapılandırmasında, Node’unda) ve sonucu size ya da ajanınıza bırakır.')],
    ov_num='01 · Genel bakış', ov_h2='Gününüz, tek bir çizelgede.',
    ov_p='Her CronJob’un çalışmaları kendi satırında, başarısızlar kırmızı, hâlâ beklenenler yalnızca çerçeveyle. Zamanda gezinmek için beş günlük bir şerit, bakılması gerekenler ve planlı bakım: alarm olarak değil, bir bant olarak.',
    ov_checks=['Kaçırılan ve uzun süren çalışmalar, gruplanmış', 'Her CronJob’un çalışma süresi kendi referans dönemine göre: “%34 daha uzun”', 'Kendi saat diliminiz ya da UTC; Türkçe ya da İngilizce'],
    ov_alt='Açık temada Genel bakış: son 24 saatin sayıları, kesintili izlemeler, başarısızların kırmızı olduğu beş günlük saatlik çalışma şeridi ve bugünün CronJob çizelgesi.',
    ex_num='02 · Explorer', ex_h2='Her kayıt, bir arama uzağınızda.',
    ex_p='Log aracınızda yaptığınız gibi filtreleyin (<code class="chip chip--ns">namespace:shop</code> <code class="chip chip--sev">önem:uyarı</code>), sonra bir hatayı açın ve ondan önceki bir saatte neyin kaydedildiğini görün.',
    before='Bundan önce kaydedilenler',
    before_items=['<span class="ago">7 dk</span> · Deployment spec’ini <b>kubectl-edit</b> yazdı',
                  '<span class="ago">12 dk</span> · Başvurduğu ConfigMap’i <b>argocd-controller</b> değiştirdi'],
    ex_alt='Açık temada Explorer: arama çubuğunda iki filtre etiketi (namespace shop ve önem uyarı), son 24 saatin histogramı ve eşleşen kayıtlar: OOMKill sonrası yeniden başlayan bir Pod, bir Warning Event ve izlenmeyen bir aralık.',
    mo_num='03 · Sabah özeti', mo_h2='Gece, gün başlamadan kanalınızda.',
    mo_p='Saat 08:30’da ya da sizin seçtiğiniz saatte OpsTide gecenin nasıl geçtiğini Slack’e, Microsoft Teams’e ya da kendi webhook’unuza gönderir: başarısız çalışmalar, başarılı çalışması geciken CronJob’lar, hemen öncesinde kaydedilen değişiklikle birlikte yeniden başlatmalar ve izlemediği süreler. Sakin bir gece de bunu söyler.',
    mo_checks=['Arayüzden iki adımda kurulur; Secret komutunu ve chart değerlerini sizin için yazar', 'Webhook URL’si bir Kubernetes Secret’ında kalır: hiç gösterilmez, log’a yazılmaz', 'OpsTide zamanında kapalıysa geç gönderilir, asla iki kez gönderilmez'],
    mo_alt='Açık temada sabah özeti kurulumu: seçmek için Slack, Microsoft Teams ya da webhook; saat, ne kadar geriye baktığı ve neleri içerdiği; yanında cluster’ın kaydından üretilmiş mesaj önizlemesi: iki başarısız çalışma, geciken bir CronJob ve OOMKill sonrası yeniden başlayan bir Pod.',
    keeps_h2='Sizin için neleri saklar',
    keeps=[('pen', 'Kim değiştirdi', 'Her spec yazımı, onu yapan istemciyle birlikte (argocd, helm, kubectl); Kubernetes’in kendi field manager kayıtlarından.'),
           ('doc', 'Yapılandırma değişiklikleri, değerler olmadan', 'ConfigMap yazımları ve değişen anahtarlar; değerleri asla. Secret’lar: yalnızca metadata ve yalnızca siz açarsanız.'),
           ('box3d', 'Release’ler ve revision’lar', 'Her rollout; Pod’ları, şablonda neyin değiştiği ve sabit bir kuralın bulgularıyla. Bir sağlık puanı değil.'),
           ('clock', 'Herhangi bir an, yeniden', 'Geçen salı saat 10:00’da hangi imajın nerede çalıştığı ve OpsTide’ın o sırada izleyip izlemediği.'),
           ('history', 'Zamanlanmış işler, dürüstçe', 'Kaçırılan başlangıçlar, 23 günlük referans döneme göre çalışma süresi ve başarılı çalışması geciken CronJob’lar; yalnızca izlediği sürece söylenir.'),
           ('cal', 'Planlı işler, etiketli', 'Helm değerlerinizdeki bakım pencereleri, içlerinde olanları işaretler. Hiçbir şey susturulmaz.')],
    fits_eyebrow='Nereye oturur', fits_h2='Altyapınızın yanında, önünde değil.',
    core_eyebrow='OpsTide · tek pod',
    core_p='list ve watch ile izler, zaman çizelgesini kendi volume’undaki SQLite’a yazar, arayüzü ve salt okunur bir API’yi sunar. Varsayılan olarak 30 gün saklar.',
    core_mono='token doğrulama · /healthz · /readyz',
    outputs=[('Kanalınız', 'isterseniz Slack’e, Teams’e ya da bir webhook’a sabah özeti'), ('Prometheus', 'alarmlar için geciken CronJob’lar ve kendi sağlığı'), ('Grafana', 'değişiklikler ve hatalar annotation olarak'), ('Ajanınız', 'sürümlü JSON kanıt dışa aktarımı')],
    sec_eyebrow='Güvenlik', sec_h2='Üretim ortamına girebilmek için tasarlandı.',
    sec_p='Kurmadan önce <a href="' + GH + '/blob/main/docs/install.md">RBAC’nin tamamını okuyun</a>. Kısa.',
    rbac=[('Verb’ler', '<span class="mono">list · watch</span>'),
          ('Asla istenmez', 'exec, log’lar, Secret verisi, herhangi bir yazma'),
          ('Secret’lar', 'Varsayılan olarak kapalı. Açıkken yalnızca metadata istenir; last-applied annotation’ı ve managedFields geldiği anda atılır. Verilen izin ise secrets üzerinde list ve watch’tır.'),
          ('Kapsam', 'Tüm cluster ya da yalnızca listelediğiniz namespace’ler'),
          ('Erişim', 'Arayüz ve API için token, ya da oauth2-proxy ile kişi bazlı giriş'),
          ('Dışarı giden', 'Hiçbir şey; sabah özetini kurarsanız yalnızca listelediğiniz hedeflere HTTPS')],
    inst_h2='İki dakikada çalışır durumda',
    demo_eyebrow='Önce deneyin · cluster yok, Docker yok',
    demo_comment='# örnek veri, 127.0.0.1:8080 adresinde açılır',
    demo_note='Go 1.26 ve arayüzü derlemek için Node 22 gerekir.',
    copy_demo='Demo komutlarını kopyala',
    helm_eyebrow='Cluster’ınıza kurun',
    helm_comment='# ardından http://127.0.0.1:8080 adresini açın',
    helm_note='<code>opstide-auth</code> Secret’ındaki token ile giriş yapın; chart’ın tüm değerleri <a href="' + GH + '/blob/main/docs/install.md">kurulum belgesinde</a>. OpsTide erken geliştirme aşamasında (v0.1): chart ilk release ile yayımlanır.',
    copy_helm='Kurulum komutlarını kopyala',
    foot_tag='Kanıtı kaydeder. Kanıtı ajanlar yorumlar.',
    foot_links=[('GitHub', GH), ('Belgeler', GH + '/tree/main/docs'), ('Sürümler', GH + '/releases')],
    license='Apache 2.0',
)


def page(s):
    L = s['lang']
    nav = '\n'.join(f'          <li><a href="#{i}">{t}</a></li>' for i, t in s['nav'])
    facts = '\n'.join(f'      <li>{ICON[i]}<div><strong>{a}</strong><span>{b}</span></div></li>' for i, a, b in s['facts'])
    tags = [f'<span class="tag-observed">{s["p"][0][0]}</span>', '<span class="tag-hatch" aria-hidden="true"></span>', f'<span class="tag-link">{s["p"][2][0]}</span>']
    princ = '\n'.join(f'      <li>\n        {tags[k]}\n        <h3>{h}</h3>\n        <p>{p}</p>\n      </li>' for k, (_, h, p) in enumerate(s['p']))
    checks = '\n'.join(f'        <li>{ICON["check"]}<span>{c}</span></li>' for c in s['ov_checks'])
    mo_checks = '\n'.join(f'        <li>{ICON["check"]}<span>{c}</span></li>' for c in s['mo_checks'])
    before = '\n'.join(f'          <li>{b}</li>' for b in s['before_items'])
    keeps = '\n'.join(f'        <li>\n          {ICON[i]}\n          <h3>{h}</h3>\n          <p>{p}</p>\n        </li>' for i, h, p in s['keeps'])
    outputs = '\n'.join(f'        <li><b>{a}</b><span>{b}</span></li>' for a, b in s['outputs'])
    rbac = '\n'.join(f'      <dt>{a}</dt><dd>{b}</dd>' for a, b in s['rbac'])
    foot = '\n'.join(f'        <li><a href="{u}">{t}</a></li>' for t, u in s['foot_links'])
    en_cur = ' aria-current="page"' if L == 'en' else ''
    tr_cur = ' aria-current="page"' if L == 'tr' else ''
    img = lambda n: f'/assets/img/{n}-{L}.webp'
    return f'''<!doctype html>
<html lang="{L}" data-copied="{s['copied']}" data-copy-failed="{s['copy_failed']}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'">
<meta name="referrer" content="strict-origin-when-cross-origin">
<title>{s['title']}</title>
<meta name="description" content="{s['description']}">
<meta name="theme-color" content="#0f1214">
<link rel="alternate" hreflang="en" href="/">
<link rel="alternate" hreflang="tr" href="/tr/">
<link rel="alternate" hreflang="x-default" href="/">
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<meta property="og:type" content="website">
<meta property="og:site_name" content="OpsTide">
<meta property="og:title" content="{s['title']}">
<meta property="og:description" content="{s['description']}">
<meta property="og:url" content="{s['home']}">
<meta property="og:image" content="{img('hero')}">
<meta property="og:image:width" content="2400">
<meta property="og:image:height" content="1500">
<meta property="og:image:alt" content="{html.escape(s['hero_alt'], quote=True)}">
<meta property="og:locale" content="{s['locale']}">
<meta property="og:locale:alternate" content="{s['other_locale']}">
<meta name="twitter:card" content="summary_large_image">
<link rel="preload" href="/assets/fonts/archivo-latin-wdth-normal.woff2" as="font" type="font/woff2" crossorigin>
<link rel="stylesheet" href="/assets/site.css">
<script src="/assets/site.js"></script>
</head>
<body>
<a class="skip-link" href="#main">{s['skip']}</a>

<header class="site-header on-night">
  <div class="wrap">
    <a class="brand" href="{s['home']}" aria-label="{s['home_label']}">
      {ICON['mark']}
      <span class="brand-name" aria-hidden="true">OpsTide</span>
    </a>
    <button class="nav-toggle" type="button" aria-expanded="false" aria-controls="site-nav">{ICON['menu']}<span>{s['menu']}</span></button>
    <nav class="site-nav" id="site-nav" aria-label="{s['site_nav']}">
      <ul>
{nav}
      </ul>
    </nav>
    <div class="header-tools">
      <nav class="lang" aria-label="{s['lang_nav']}">
        <ul>
          <li><a href="/" hreflang="en" lang="en"{en_cur}>EN</a></li>
          <li><a href="/tr/" hreflang="tr" lang="tr"{tr_cur}>TR</a></li>
        </ul>
      </nav>
      <a class="btn-outline" href="{GH}">{ICON['github']}<span class="label">GitHub</span></a>
    </div>
  </div>
</header>

<main id="main">
<section class="hero on-night" aria-labelledby="hero-title">
  <div class="wrap">
    <div class="hero-copy">
      <p class="pill">{s['pill']}</p>
      <h1 id="hero-title">{s['h1']}</h1>
      <p class="lead">{s['lead']}</p>
      <div class="hero-actions">
        <div class="cmd">
          <div class="cmd-text" role="region" aria-label="{s['cmd_label']}" tabindex="0"><span class="prompt" aria-hidden="true">$</span><code id="cmd-helm{s['prefix']}">{HELM}</code></div>
          <button class="copy" type="button" data-copy="cmd-helm{s['prefix']}" aria-label="{s['copy_cmd']}">{ICON['copy']}</button>
        </div>
        <a class="btn-primary" href="#demo">{s['cta']} {ICON['arrow']}</a>
      </div>
    </div>
    <figure class="window">
      <div class="window-bar" aria-hidden="true"><span class="dot"></span><span class="dot"></span><span class="dot"></span><span class="url">opstide.internal/explorer</span></div>
      <img src="{img('hero')}" width="2400" height="1500" alt="{s['hero_alt']}" fetchpriority="high" decoding="async">
    </figure>
  </div>
</section>

<section class="facts" aria-label="{s['glance']}">
  <ul class="wrap">
{facts}
  </ul>
</section>

<section class="principles wrap" id="how" aria-labelledby="how-title">
  <p class="eyebrow">{s['p_eyebrow']}</p>
  <h2 id="how-title">{s['p_h2']}</h2>
  <ol>
{princ}
  </ol>
</section>

<section class="feature wrap" id="features" aria-labelledby="ov-title">
  <div class="feature-copy">
    <p class="num">{s['ov_num']}</p>
    <h2 id="ov-title">{s['ov_h2']}</h2>
    <p>{s['ov_p']}</p>
    <ul class="checks">
{checks}
    </ul>
  </div>
  <figure class="shot">
    <img src="{img('overview')}" width="2400" height="1500" alt="{s['ov_alt']}" loading="lazy" decoding="async">
  </figure>
</section>

<section class="feature feature--reverse wrap" aria-labelledby="ex-title">
  <div class="feature-copy">
    <p class="num">{s['ex_num']}</p>
    <h2 id="ex-title">{s['ex_h2']}</h2>
    <p>{s['ex_p']}</p>
    <div class="before">
      <h3 class="eyebrow" id="before-title">{s['before']}</h3>
      <ul aria-labelledby="before-title">
{before}
      </ul>
    </div>
  </div>
  <figure class="shot">
    <img src="{img('explorer')}" width="2400" height="1500" alt="{s['ex_alt']}" loading="lazy" decoding="async">
  </figure>
</section>

<section class="feature wrap" aria-labelledby="mo-title">
  <div class="feature-copy">
    <p class="num">{s['mo_num']}</p>
    <h2 id="mo-title">{s['mo_h2']}</h2>
    <p>{s['mo_p']}</p>
    <ul class="checks">
{mo_checks}
    </ul>
  </div>
  <figure class="shot">
    <img src="{img('morning')}" width="2400" height="1500" alt="{s['mo_alt']}" loading="lazy" decoding="async">
  </figure>
</section>
<section class="keeps" aria-labelledby="keeps-title">
  <div class="wrap">
    <h2 id="keeps-title">{s['keeps_h2']}</h2>
    <ul>
{keeps}
    </ul>
  </div>
</section>

<section class="fits wrap" id="integrations" aria-labelledby="fits-title">
  <p class="eyebrow">{s['fits_eyebrow']}</p>
  <h2 id="fits-title">{s['fits_h2']}</h2>
  <div class="flow">
    <div class="box">
      <h3 class="eyebrow">Kubernetes API</h3>
      <ul>
        <li>CronJobs · Jobs · Pods</li>
        <li>Deployments · ReplicaSets</li>
        <li>StatefulSets · DaemonSets</li>
        <li>Events · Nodes · HPAs</li>
        <li>ConfigMaps</li>
      </ul>
    </div>
    <div class="arrow">{ICON['flow']}</div>
    <div class="box box--core">
      <h3 class="eyebrow">{s['core_eyebrow']}</h3>
      <p>{s['core_p']}</p>
      <p class="mono">{s['core_mono']}</p>
    </div>
    <div class="arrow">{ICON['flow']}</div>
    <ul class="outputs">
{outputs}
    </ul>
  </div>
</section>

<section class="security on-night" id="security" aria-labelledby="sec-title">
  <div class="wrap">
    <div>
      <p class="eyebrow">{s['sec_eyebrow']}</p>
      <h2 id="sec-title">{s['sec_h2']}</h2>
      <p>{s['sec_p']}</p>
    </div>
    <dl class="rbac">
{rbac}
    </dl>
  </div>
</section>

<section class="install wrap" id="install" aria-labelledby="inst-title">
  <h2 id="inst-title">{s['inst_h2']}</h2>
  <div class="install-grid">
    <div id="demo">
      <h3 class="eyebrow" id="demo-title">{s['demo_eyebrow']}</h3>
      <div class="code">
        <pre tabindex="0" aria-labelledby="demo-title"><code id="code-demo{s['prefix']}"><span class="comment">{s['demo_comment']}</span>
git clone {GH}
cd opstide &amp;&amp; make demo-local</code></pre>
        <button class="copy" type="button" data-copy="code-demo{s['prefix']}" aria-label="{s['copy_demo']}">{ICON['copy']}</button>
      </div>
      <p class="note">{s['demo_note']}</p>
    </div>
    <div>
      <h3 class="eyebrow" id="helm-title">{s['helm_eyebrow']}</h3>
      <div class="code">
        <pre tabindex="0" aria-labelledby="helm-title"><code id="code-helm{s['prefix']}">helm install opstide oci://ghcr.io/mihnk/charts/opstide \\
  -n opstide --create-namespace
kubectl -n opstide port-forward svc/opstide 8080:80
<span class="comment">{s['helm_comment']}</span></code></pre>
        <button class="copy" type="button" data-copy="code-helm{s['prefix']}" aria-label="{s['copy_helm']}">{ICON['copy']}</button>
      </div>
      <p class="note">{s['helm_note']}</p>
    </div>
  </div>
  <p class="sr-only" id="copy-status" role="status" aria-live="polite"></p>
</section>
</main>

<footer class="site-footer">
  <div class="wrap">
    <span class="brand-name">OpsTide</span>
    <span>{s['foot_tag']}</span>
    <span class="spacer"></span>
    <nav aria-label="{'Project' if L == 'en' else 'Proje'}">
      <ul>
{foot}
      </ul>
    </nav>
    <span>{s['license']}</span>
  </div>
</footer>
</body>
</html>
'''


(ROOT / 'index.html').write_text(page(EN))
(ROOT / 'tr').mkdir(exist_ok=True)
(ROOT / 'tr' / 'index.html').write_text(page(TR))
print('ok')
