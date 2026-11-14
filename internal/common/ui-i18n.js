// SlipDrukka panels: English for the interface. The pages are written in
// Ukrainian; in English mode every text that reaches the screen (and the
// texts of confirm/prompt/alert) is translated here: exact texts first,
// then patterns for texts with values in them. Names of shops and printers
// are left as they are.
(function(){
const pick=()=>{const q=new URLSearchParams(location.search).get('lang');
  if(q==='en'||q==='uk'){try{localStorage.setItem('sd_lang',q)}catch(e){}return q}
  try{const v=localStorage.getItem('sd_lang');if(v==='en'||v==='uk')return v}catch(e){}
  return (navigator.language||'').toLowerCase().startsWith('uk')?'uk':'en'};
const LANG=pick();
window.PR_LANG=LANG;
window.prSetLang=l=>{try{localStorage.setItem('sd_lang',l)}catch(e){}location.reload()};

const X={
// frame, sign-in
'SlipDrukka — сервер':'SlipDrukka — server','SlipDrukka — магазин':'SlipDrukka — shop',
'Увійдіть обліковим записом адміністратора Windows цього сервера, тим самим, яким заходите по RDP.':'Sign in with a Windows administrator account of this server — the same one you use for RDP.',
"Ім'я користувача":'User name',"Administrator, ДОМЕН\\ім'я або ім'я@домен":'Administrator, DOMAIN\\name or name@domain','Пароль':'Password','Увійти':'Sign in',
'Панель відкрито не з самого сервера, тому вхід обліковим записом Windows тут недоступний.':'The panel is not opened on the server itself, so Windows sign-in is not available here.',
"Налаштовано аварійний пароль панелі: щоб увійти ним, залиште ім'я порожнім.":'An emergency panel password is set: to use it, leave the user name empty.',
'Магазини':'Shops','Налаштування':'Settings','Журнал':'Journal','Додати магазин':'Add shop','Довідка':'Help','Вийти':'Sign out','Як це працює':'How it works',
// status
'Усе друкує':'All printing','Потрібна увага':'Needs attention','Магазинів ще немає':'No shops yet',
"Додайте перший магазин: панель видасть код, з яким програма на його комп'ютері сама знайде цей сервер.":'Add the first shop: the panel gives a code with which the program on its computer finds this server on its own.',
'черга порожня':'queue empty','Зрозуміло':'OK',
// shop card
'Перейменувати магазин':'Rename shop','Додати принтер':'Add printer','Ще':'More','Видати новий код підключення':'Issue a new connection code',
"Прив'язати код до IP магазину":"Tie the code to the shop's IP",'Видалити магазин':'Delete shop',
'немає зв\'язку':'offline','Захищений тунель WireGuard усередині програми':'Protected WireGuard tunnel inside the program',"Звичайне з'єднання TLS":'Regular TLS connection',
'Друк з програми':'Printing from programs','Друкує в магазині':'Prints in the shop','Підключення':'Connection','Що друкує':'What it prints','Стан':'State',
'Чек / етикетка':'Receipt / label','Документ':'Document','Що друкує цей принтер':'What this printer prints',
'Перейменувати принтер на сервері':'Rename the printer on the server','Перейменувати принтер у магазині':'Rename the printer in the shop','Змінити IP-адресу принтера':"Change the printer's IP address",
'Створити принтер на сервері':'Create printer on the server','Мережа':'Network','мережа, WSD':'network, WSD',
'друкує':'printing','готовий':'ready','не друкує':'not printing','не видано':'not shared','немає принтера на сервері':'no printer on the server',"магазин не на зв'язку":'shop offline',
'ще нічого не друкував':'nothing printed yet','Прибрати':'Remove','Звільнити порт':'Free the port','поставити Generic':'set Generic',
'порт закріплено за ним, доки його не звільнять':'the port stays reserved for it until freed',
'Його немає у Windows магазину':"It is not in the shop's Windows",'Його немає у Windows магазину, чеки йдуть через службову чергу':"It is not in the shop's Windows; receipts go through a service queue",
'Драйвер v4, тому чеки йдуть через службову чергу':'v4 driver, so receipts go through a service queue',
'Принтерів ще немає. Натисніть «Додати принтер», щоб видати принтер цього магазину.':'No printers yet. Press “Add printer” to share a printer of this shop.',
'Принтерів ще немає. Їх можна буде додати, коли магазин підключиться.':'No printers yet. They can be added once the shop connects.',
'Принтер':'Printer','Надіслано':'Sent','Розмір':'Size','Спроб':'Attempts','Що заважає':'What is in the way','Скасувати':'Cancel',
'Код підключення протермінований: ним так і не скористались. Видайте новий (Ще → Видати новий код підключення).':'The connection code has expired without being used. Issue a new one (More → Issue a new connection code).',
'код підключення протермінований — видайте новий (Ще → Видати новий код підключення)':'the connection code has expired — issue a new one (More → Issue a new connection code)',
// settings
'Кожен блок зберігається своєю кнопкою. Магазини перепідключаться самі.':'Each block is saved with its own button. Shops reconnect on their own.',
'Як магазини знаходять сервер':'How shops find the server','Ця адреса вшивається в код підключення кожного магазину.':"This address is built into every shop's connection code.",
'Зовнішня адреса сервера':'Public server address',"IP або ім'я, за яким сервер видно з інтернету, і порт для магазинів. Наприклад 203.0.113.10:7310":'The IP or name under which the server is reachable from the internet, and the port for shops. For example 203.0.113.10:7310',
'Порт для магазинів':'Port for shops','Його треба прокинути на роутері сервера. Після зміни змініть і перенаправлення.':"It must be forwarded on the server's router. After a change, change the forwarding too.",
'Зберегти':'Save','Застосувати':'Apply','Збережено':'Saved','Застосовано':'Applied',
'Звідки дозволено підключатися':'Where connections are allowed from',
'Зовнішні IP-адреси магазинів або мережі, по одній на рядок. Порожньо — з будь-якої адреси. Стосується лише порту для магазинів; панель і RDP не зачіпає.':"The shops' public IP addresses or networks, one per line. Empty — from anywhere. Applies only to the port for shops; the panel and RDP are not affected.",
'Дозволені адреси':'Allowed addresses','Додати адреси підключених зараз магазинів':'Add the addresses of shops connected now','Зараз жоден магазин не підключений.':'No shop is connected now.',
'Кого не пустили':'Refused','Адреса':'Address','Остання':'Last','Причина':'Reason','Дозволити':'Allow',
'Канал WireGuard':'WireGuard channel',
'Додатковий захищений канал до магазинів, вбудований у програму: без адаптерів і драйверів у Windows, інші VPN на сервері й у магазинах не зачіпає. Порт той самий, що й для магазинів, але UDP — його теж треба прокинути на роутері сервера. Якщо в магазині UDP не проходить, програма сама підключається звичайним способом.':"An extra protected channel to the shops, built into the program: no adapters or drivers in Windows, other VPNs on the server and in the shops are not touched. Same port as for shops, but UDP — forward it on the server's router too. Where UDP does not get through, the shop program connects the regular way on its own.",
'Режим':'Mode','увімкнено (рекомендовано)':'on (recommended)','вимкнено':'off','лише WireGuard':'WireGuard only',
"«Увімкнено»: WireGuard, де він проходить, інакше звичайне з'єднання. «Лише WireGuard»: звичайний порт закрито, магазини без UDP не підключаться.":'“On”: WireGuard where it gets through, otherwise the regular connection. “WireGuard only”: the regular port is closed, shops without UDP cannot connect.',
'Вимкнено.':'Off.','Не запустився — дивіться журнал сервера.':'Did not start — see the server journal.',
'Захист від підбору і сканерів':'Protection against guessing and scanners',
'Адреса, з якої за 10 хвилин було 5 невдалих підключень (сміття замість рукостискання, чужий або протермінований код), блокується. Адреси зі списку дозволених і ті, з яких за останні 30 днів підключався магазин, не блокуються ніколи. Якщо з заблокованої адреси прийде дійсний код магазину, блокування зніметься само.':'An address with 5 failed connections in 10 minutes (garbage instead of a handshake, a foreign or expired code) is blocked. Allow-listed addresses and addresses a shop connected from in the last 30 days are never blocked. If a valid shop code comes from a blocked address, the block is lifted on its own.',
'На скільки блокувати адресу':'How long to block an address','1 годину':'1 hour','1 добу':'1 day','5 днів':'5 days','30 днів':'30 days','назавжди':'for good','7 днів':'7 days','без терміну':'no limit',
'Скільки діє код підключення, поки ним не скористались':'How long a connection code is valid until used',
"Прив'язувати код нового магазину до першої адреси, з якої він підключиться":"Tie a new shop's code to the first address it connects from",
"Зручно, якщо в магазинів незмінні адреси. Якщо провайдер змінить адресу, магазин доведеться прив'язати заново (Ще → Прив'язати код до IP).":"Handy when shops have fixed addresses. If the provider changes the address, the shop has to be tied again (More → Tie the code to the shop's IP).",
'Заблокованих адрес немає.':'No blocked addresses.','Адреса і причина':'Address and reason','Блок':'Block','Розблокувати':'Unblock',
"Необов'язково, для тих, у кого вже стоїть CrowdSec. Готові правила для нього — у теці":'Optional, for those who already run CrowdSec. Ready-made rules for it are in the',
'репозиторію SlipDrukka.':'folder of the SlipDrukka repository.',
'Писати невдалі підключення в журнал для CrowdSec':'Write failed connections to a log for CrowdSec','Не пускати адреси, які заблокував CrowdSec':'Do not let in addresses CrowdSec blocked',
'Адреси магазинів і зі списку дозволених не блокуються ніколи.':'Shop addresses and allow-listed addresses are never blocked.','Адреса локального API CrowdSec':'CrowdSec Local API address',
'Ключ API':'API key','Створіть на машині з CrowdSec:':'Create it on the machine with CrowdSec:','вставте ключ':'paste the key','ключ задано — залиште порожнім, щоб не міняти':'key set — leave empty to keep it',
'Підключаюсь до CrowdSec…':'Connecting to CrowdSec…',
'Черга чеків':'Receipt queue',"Поки магазин без зв'язку, чеки чекають на диску сервера і друкуються, щойно він повернеться. Старіші за цей строк прибираються, щоб зранку не вийшли вчорашні.":"While a shop is offline, receipts wait on the server's disk and print as soon as it is back. Older ones are dropped, so yesterday's receipts do not come out in the morning.",
'Скільки чек чекає магазин, хвилин':'How long a receipt waits for the shop, minutes',
'Панель і порти принтерів':'Panel and printer ports','Хто може відкрити цю панель':'Who can open this panel','лише цей сервер':'only this server','уся локальна мережа':'the whole local network',
'Вхід обліковим записом Windows працює лише на самому сервері, щоб пароль не йшов мережею.':'Windows sign-in works only on the server itself, so the password never travels over the network.',
'Порт панелі':'Panel port','З якого номера давати порти новим принтерам':'First port number for new printers','Кожен принтер магазину отримує на сервері свій порт 127.0.0.1:номер.':'Each shop printer gets its own port 127.0.0.1:number on the server.',
'Мова журналів':'Log language','Мова, якою сервер і магазини пишуть свої журнали. Панель показує все мовою, вибраною в ній самій.':'The language the server and the shops write their logs in. The panel shows everything in the language chosen in it.',
'українська':'Ukrainian','англійська':'English',
// journal
'Дії в панелі і все, що сервер записав у свій журнал.':'Actions in the panel and everything the server wrote to its journal.','Дії адміністратора':'Admin actions',
'Хто, коли і з якої адреси щось змінював у панелі. Зберігається у файлі audit.log поруч із налаштуваннями сервера.':"Who changed what in the panel, when and from which address. Kept in audit.log next to the server's settings.",
'Коли':'When','Хто':'Who','Звідки':'From','Що':'What','Ще нічого не змінювали.':'Nothing changed yet.','Журнал сервера':'Server journal',
// dialogs
'Закрити':'Close','Панель створить код підключення. З ним програма на комп\'ютері магазину сама знайде цей сервер, і на роутері магазину нічого налаштовувати не треба.':"The panel creates a connection code. With it the program on the shop computer finds this server on its own, and nothing has to be set up on the shop's router.",
'Створити код':'Create code','Копіювати команду':'Copy command','Копіювати код':'Copy code','Скопійовано':'Copied',
'Скопіюйте команду встановлення і виконайте її на комп\'ютері магазину від імені адміністратора, поруч із файлом програми:':'Copy the install command and run it on the shop computer as an administrator, next to the program file:',
'Або, якщо програма вже стоїть, вставте код на її сторінці (значок біля годинника):':'Or, if the program is already installed, paste the code on its page (the icon next to the clock):',
"Магазин з'явиться в панелі сам, щойно програма підключиться.":'The shop appears in the panel on its own once the program connects.',
'Спершу вкажіть зовнішню адресу сервера у «Налаштуваннях», інакше магазин не знатиме, куди підключатися.':'First set the public server address in “Settings”, otherwise the shop will not know where to connect.',
'Принтер на сервері':'Printer on the server','Назва, яку бачитимуть програми':'Name programs will see','Драйвер на сервері':'Driver on the server','завантаження…':'loading…','Створити принтер':'Create printer','створюю…':'creating…',
'Виберіть драйвер моделі принтера, встановлений на сервері. Саме він малює чек, QR-код і штрихкоди.':"Choose the printer model's driver installed on the server. It renders the receipt, QR code and barcodes.",
'Режим «Документ»: на сервері ставиться Microsoft XPS Document Writer v4, а друкує рідний драйвер у магазині. Користувачі бачать лише назву.':"“Document” mode: the server uses Microsoft XPS Document Writer v4, and the printer's own driver in the shop prints. Users see only the name.",
'Уже є у Windows магазину':"Already in the shop's Windows",'Мережевий, за IP':'Network, by IP','Що друкуватиме':'What it will print','Драйвер':'Driver','Порт':'Port','Видати':'Share',
"Усі принтери цього комп'ютера вже видані на сервер.":"All printers of this computer are already shared with the server.",
'Назва принтера':'Printer name','IP-адреса і порт':'IP address and port','Драйвер у магазині':'Driver in the shop',
'Для чеків і етикеток досить Generic / Text Only: чек малює драйвер на сервері.':'Generic / Text Only is enough for receipts and labels: the receipt is rendered by the driver on the server.',
'Показати драйвери магазину':"Show the shop's drivers",'Що він друкує':'What it prints','Створити і видати':'Create and share',
'Windows бачить підключені USB-принтери:':'Windows sees connected USB printers:','. Який із них на якому порту, покаже тестовий друк.':'. A test print shows which one is on which port.',
'Windows не повідомляє моделі підключених USB-принтерів. Знайдіть порт тестовим друком: на потрібному принтері вийде короткий текст.':'Windows does not report the models of connected USB printers. Find the port with a test print: a short text comes out on the right printer.',
'Уже на ньому':'Already on it','Тестовий друк':'Test print','Вибрати':'Choose','USB-портів принтерів немає.':'No USB printer ports.',
'Принтер буде створено з вбудованим драйвером Generic / Text Only.':'The printer will be created with the built-in Generic / Text Only driver.',
'Виконую в магазині…':'Working at the shop…','Готово. Тепер створіть для нього принтер на сервері.':'Done. Now create a printer on the server for it.',
'Назва, наприклад «Магазин на Січових»':'Name, for example “Main Street shop”','Етикетки XP-370B':'Labels XP-370B','Чек XP-80C':'Receipt XP-80C',
// shop page
'Друк працює':'Printing works',"Немає зв'язку з сервером":'No connection to the server','Магазин ще не підключено':'Shop not connected yet',
'Підключення до сервера':'Connection to the server','Вставте код, який видав адміністратор у панелі сервера.':'Paste the code the administrator issued in the server panel.',
'Код підключення':'Connection code','Підключити':'Connect','Збережено, підключаюсь…':'Saved, connecting…',
'Принтери, на які друкує сервер':'Printers the server prints to','Принтер у Windows':'Printer in Windows','Назва на сервері':'Name on the server',
'Поки жоден принтер не видано.':'No printer shared yet.','Ця сторінка лише показує стан. Принтери додає й налаштовує адміністратор зі свого сервера.':'This page only shows the status. The administrator adds and sets up printers from the server.',
'Що відбувалося':'What happened','Події':'Events','Розділи':'Sections','Поки нічого не відбувалося.':'Nothing has happened yet.','Технічний журнал':'Technical log','Вставте код підключення нижче.':'Paste the connection code below.',
"Програма сама підключиться, щойно з'явиться інтернет або запрацює сервер. Чеки тим часом чекають на сервері.":'The program reconnects on its own once the internet or the server is back. Receipts wait on the server meanwhile.',
// confirm / prompt / alert
'Нова назва магазину:':'New shop name:','Скасувати цей чек? Він не надрукується.':'Cancel this receipt? It will not print.',
'Нова назва принтера у Windows магазину:':"New printer name in the shop's Windows:",
"Нова назва принтера на сервері.\n\nУвага: програми запам'ятовують принтер за назвою, після перейменування в них треба буде вибрати принтер знову.":'New printer name on the server.\n\nNote: programs remember a printer by name; after renaming it has to be chosen in them again.',
'Формат: 192.168.1.50 або 192.168.1.50:9100':'Format: 192.168.1.50 or 192.168.1.50:9100',
'Панель переїде на інший порт і відкриється за новою адресою. Продовжити?':'The panel moves to another port and opens at the new address. Continue?',
"Звичайний порт для магазинів буде закрито. Магазини, де UDP не проходить, втратять зв'язок. Продовжити?":'The regular port for shops will be closed. Shops where UDP does not get through will lose the connection. Continue?',
};
const P=[
[/^(\d+) з (\d+) (?:магазину|магазинів) на зв'язку, (\d+) (?:принтер|принтери|принтерів), (.+)\.$/,(m)=>`${m[1]} of ${m[2]} ${m[2]==1?'shop':'shops'} connected, ${m[3]} ${m[3]==1?'printer':'printers'}, ${tr(m[4])}.`],
[/^(\d+) (?:чек у черзі|чеки в черзі|чеків у черзі)$/,m=>`${m[1]} ${m[1]==1?'receipt':'receipts'} in queue`],
[/^(\d+) (?:чек чекає|чеки чекають|чеків чекають)$/,m=>`${m[1]} ${m[1]==1?'receipt':'receipts'} waiting`],
[/^немає зв'язку, (\d+) (?:чек чекає|чеки чекають|чеків чекають)$/,m=>`offline, ${m[1]} ${m[1]==1?'receipt':'receipts'} waiting`],
[/^: (.*)$/,m=>': '+tr(m[1])],
[/^«(.+?)»: створіть для нього принтер на сервері$/,m=>`“${m[1]}”: create a printer on the server for it`],
[/^«(.+?)»: (.+)$/,m=>`“${m[1]}”: ${tr(m[2])}`],
[/^на зв'язку з (.+)$/,m=>`connected since ${m[1]}`],[/^на зв'язку$/,()=>'connected'],
[/^(\d+) надруковано$/,m=>`${m[1]} printed`],
[/^(\d+) надруковано, (\d+) (?:помилка|помилки|помилок)$/,m=>`${m[1]} printed, ${m[2]} ${m[2]==1?'error':'errors'}`],
[/^останній (.+)$/,m=>`last ${tr(m[1])}`],[/^щойно$/,()=>'just now'],[/^(\d+) хв тому$/,m=>`${m[1]} min ago`],[/^(\d+) год тому$/,m=>`${m[1]} h ago`],
[/^порт (.+)$/,m=>`port ${m[1]}`],[/^([\d.]+) КБ$/,m=>`${m[1]} KB`],
[/^Комп'ютер (.+), адреса (.+), програма (.+)\.$/,m=>`Computer ${m[1]}, address ${m[2]}, program ${m[3]}.`],
[/^Код діє лише з (.+)\.$/,m=>`The code works only from ${m[1]}.`],
[/^Код підключення ще не використано, він діє до (.+)\.$/,m=>`The connection code is not used yet; it is valid until ${m[1]}.`],
[/^Події: (\d+), остання о (.+)$/,m=>`Events: ${m[1]}, last at ${m[2]}`],
[/^Чеки в черзі \((\d+)\)$/,m=>`Receipts in queue (${m[1]})`],
[/^Заблоковані адреси \((\d+)\)$/,m=>`Blocked addresses (${m[1]})`],
[/^до (.+)$/,m=>`until ${m[1]}`],[/^з (\d.+)$/,m=>`since ${m[1]}`],
[/^(.+?); відкинуто ще (\d+)$/,m=>`${tr(m[1])}; ${m[2]} more dropped`],
[/^Зараз підключені: (.+)$/,m=>`Connected now: ${m[1]}`],
[/^Працює на UDP (\d+)\. Через WireGuard зараз (\d+) з (\d+) (?:підключеного магазину|підключених магазинів)\.(.*)$/,m=>`Running on UDP ${m[1]}. Over WireGuard now: ${m[2]} of ${m[3]} connected ${m[3]==1?'shop':'shops'}.${tr(m[4])}`],
[/^ Ще не знаю ключа WireGuard: (.+) — з'явиться після першого підключення цієї версії\. Поки вони є, режим «лише WireGuard» недоступний\.$/,m=>` WireGuard key not known yet for: ${m[1]} — it appears after their first connection with this version. Until then “WireGuard only” is unavailable.`],
[/^Синхронізовано о (.+), заблоковано адрес і мереж: (\d+)\.$/,m=>`Synced at ${m[1]}, blocked addresses and networks: ${m[2]}.`],
[/^Файл: (.+)$/,m=>`File: ${m[1]}`],
[/^версія (.+)$/,m=>`version ${m[1]}`],
[/^Додати принтер у «(.+)»$/,m=>`Add a printer to “${m[1]}”`],
[/^Новий код для «(.+)»$/,m=>`New code for “${m[1]}”`],
[/^Віртуальні принтери \(PDF, факс, OneNote\) не показано: (\d+)\.$/,m=>`Virtual printers (PDF, fax, OneNote) not shown: ${m[1]}.`],
[/^(.+) Друкуватиме на «(.+)» у магазині «(.+)»\.$/,m=>`${tr(m[1])} It will print to “${m[2]}” at the shop “${m[3]}”.`],
[/^Код треба використати протягом (\d+) (?:доби|діб), інакше він перестане діяти\. Після першого підключення він не старіє\.$/,m=>`The code must be used within ${m[1]} ${m[1]==1?'day':'days'}, otherwise it stops working. Once used, it does not expire.`],
[/^Дозволити лише з (.+)$/,m=>`Allow only from ${m[1]}`],
[/^підключився з нової адреси (.+) о (.+) \(раніше: (.*)\)\. Якщо це не ви змінили провайдера чи комп'ютер — видайте магазину новий код\.$/,m=>`connected from a new address ${m[1]} at ${m[2]} (before: ${m[3]}). If you did not change the provider or computer, issue the shop a new code.`],
[/^Друкую на (.+)…$/,m=>`Printing to ${m[1]}…`],
[/^Надіслано на (.+)\. Подивіться, який принтер надрукував\.$/,m=>`Sent to ${m[1]}. See which printer printed.`],
[/^(Підключено до сервера як .+?\.) (Надруковано .+)$/,m=>`${tr(m[1])} ${tr(m[2])}`],
[/^Підключено до сервера як «(.+)» з (.+?)( через WireGuard)?\.$/,m=>`Connected to the server as “${m[1]}” since ${m[2]}${m[3]?' over WireGuard':''}.`],
[/^Надруковано (\d+)(?:, помилок (\d+))?\.$/,m=>`Printed: ${m[1]}${m[2]?`, errors: ${m[2]}`:''}.`],
[/^(.+) Причина: (.+)\.$/,m=>`${tr(m[1])} Reason: ${m[2]}.`],
[/^Видати новий код для «(.+)»\? Старий одразу перестане діяти, і магазин відключиться, доки на ньому не вставлять новий\.$/,m=>`Issue a new code for “${m[1]}”? The old one stops working at once, and the shop disconnects until the new one is entered there.`],
[/^Видалити магазин «(.+)»\? Його принтери перестануть друкувати, а чеки в черзі буде скасовано\.$/,m=>`Delete the shop “${m[1]}”? Its printers stop printing, and queued receipts are cancelled.`],
[/^З яких IP-адрес дозволено код магазину «(.+)»\? Через кому\. Порожньо — з будь-якої\.(?:\nЗараз магазин підключений з (.+))?$/,m=>`Which IP addresses may use the code of shop “${m[1]}”? Comma-separated. Empty — any.${m[2]?`\nThe shop is connected from ${m[2]} now`:''}`],
[/^Прибрати «(.+)» з сервера\? Сам принтер у магазині лишиться, а програми перестануть на нього друкувати\.$/,m=>`Remove “${m[1]}” from the server? The printer itself stays in the shop, and programs stop printing to it.`],
[/^Поставити принтеру «(.+)» у магазині вбудований драйвер «Generic \/ Text Only»\?\n\nДля чеків і етикеток це нормально: чек малює драйвер на сервері\. Службова черга після цього прибереться сама\.$/,m=>`Give printer “${m[1]}” in the shop the built-in “Generic / Text Only” driver?\n\nFine for receipts and labels: the receipt is rendered by the driver on the server. The service queue then goes away on its own.`],
[/^Нова IP-адреса принтера «(.+)»\. Порт через двокрапку, зазвичай 9100\.\nПрограми нічого не помітять: назва і порт на сервері ті самі\.$/,m=>`New IP address of printer “${m[1]}”. Port after a colon, usually 9100.\nPrograms notice nothing: the name and port on the server stay the same.`],
[/^Звільнити порт 127\.0\.0\.1:(\d+)\?\n\nЯкщо цей принтер колись видадуть знову, він отримає новий номер\. Чеки, що чекали на нього, буде скасовано\.$/,m=>`Free port 127.0.0.1:${m[1]}?\n\nIf this printer is shared again some day, it gets a new number. Receipts waiting for it are cancelled.`],
[/^Після збереження ВІДКЛЮЧАТЬСЯ ці магазини, бо їхніх адрес немає в списку:\n\n([\s\S]+)\n\nВсе одно зберегти\?$/,m=>`After saving, these shops WILL DISCONNECT because their addresses are not on the list:\n\n${m[1]}\n\nSave anyway?`],
];
function tr(s){
  if(LANG!=='en'||!s)return s;
  // English quotes go in after the lookup: the catalog and the patterns are
  // written with «», and swapping first made every «»-pattern miss (0.15.0).
  const q=t=>/[«»]/.test(t)?t.replace(/«/g,'“').replace(/»/g,'”'):t;
  if(!/[Ѐ-ӿ]/.test(s))return q(s);
  if(X[s]!==undefined)return q(X[s]);
  for(const [re,f] of P){const m=s.match(re);if(m)return q(f(m))}
  return q(s);
}
window.T=tr;
if(LANG!=='en')return;
// plural forms: English needs only one/other
window.prPlural=(n,one,few,many)=>n==1?one:many;
const fix=n=>{
  if(n.nodeType===3){const v=n.nodeValue,t=v.trim();if(!t)return;const r=tr(t.replace(/\s+/g,' '));if(r!==t)n.nodeValue=v.replace(t,r);return}
  if(n.nodeType!==1||n.tagName==='SCRIPT'||n.tagName==='STYLE'||n.tagName==='PRE')return;
  for(const a of ['placeholder','title','aria-label']){const v=n.getAttribute&&n.getAttribute(a);if(v){const r=tr(v);if(r!==v)n.setAttribute(a,r)}}
  n.childNodes.forEach(fix);
};
const run=()=>{document.title=tr(document.title);fix(document.body)};
document.addEventListener('DOMContentLoaded',()=>{run();new MutationObserver(ms=>{for(const m of ms){if(m.type==='characterData')fix(m.target);else m.addedNodes.forEach(fix)}}).observe(document.body,{childList:true,subtree:true,characterData:true})});
// attributes set after load (placeholders, titles)
document.addEventListener('DOMContentLoaded',()=>new MutationObserver(ms=>{for(const m of ms){const n=m.target,v=n.getAttribute(m.attributeName);if(v){const r=tr(v);if(r!==v)n.setAttribute(m.attributeName,r)}}}).observe(document.body,{attributes:true,subtree:true,attributeFilter:['placeholder','title','aria-label']}));
const C=window.confirm.bind(window),Pr=window.prompt.bind(window),A=window.alert.bind(window);
window.confirm=m=>C(tr(m));window.prompt=(m,d)=>Pr(tr(m),d);window.alert=m=>A(tr(m));
})();
