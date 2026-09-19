// Package mailtext holds the account emails in each language the website
// speaks (DE, FR, PT-BR, IT, ES, English). The language comes from the
// request's Accept-Language, which the website sets to the page language;
// terms follow the website's docs/glossary.md.
package mailtext

import "strings"

// Mail kinds.
const (
	Verify            = "verify"
	EmailChange       = "email_change"        // to the new address
	EmailChangeNotice = "email_change_notice" // to the old address
	Reset             = "reset"
)

type mail struct{ subject, body string }

// Bodies use {hi} (the greeting's name part: " Ana" or "") and {link}.
var mails = map[string]map[string]mail{
	"en": {
		Verify: {"Confirm your OpenPak email",
			"Hi{hi},\n\nConfirm your OpenPak account email address:\n{link}\n\nIf you did not create this account, ignore this message."},
		EmailChange: {"Confirm your new OpenPak email address",
			"Hi{hi},\n\nOpen this link to make this the email address of your OpenPak account:\n{link}\n\nThe link expires in 24 hours and can be used once. If you did not ask for this, ignore this message."},
		EmailChangeNotice: {"Your OpenPak email address is being changed",
			"Hi{hi},\n\nSomeone signed in to your OpenPak account asked to move it to a new email address. Nothing changes until the link sent to that address is opened.\n\nIf this was not you, change your password now: it also signs out every other session."},
		Reset: {"Reset your OpenPak password",
			"A password reset was requested for your OpenPak account.\n\nOpen this link to choose a new password:\n{link}\n\nThe link expires in 24 hours and can be used once.\nIf you did not request this, ignore this message; your password is unchanged."},
	},
	"de": {
		Verify: {"Bestätige deine E-Mail-Adresse für OpenPak",
			"Hallo{hi},\n\nbestätige die E-Mail-Adresse deines OpenPak-Kontos:\n{link}\n\nWenn du dieses Konto nicht erstellt hast, ignoriere diese Nachricht."},
		EmailChange: {"Bestätige deine neue E-Mail-Adresse für OpenPak",
			"Hallo{hi},\n\nöffne diesen Link, um diese Adresse zur E-Mail-Adresse deines OpenPak-Kontos zu machen:\n{link}\n\nDer Link ist 24 Stunden gültig und kann einmal verwendet werden. Wenn du das nicht angefordert hast, ignoriere diese Nachricht."},
		EmailChangeNotice: {"Die E-Mail-Adresse deines OpenPak-Kontos wird geändert",
			"Hallo{hi},\n\njemand, der in deinem OpenPak-Konto angemeldet ist, möchte es auf eine neue E-Mail-Adresse umstellen. Nichts ändert sich, bevor der Link geöffnet wird, der an diese Adresse gesendet wurde.\n\nWenn du das nicht warst, ändere jetzt dein Passwort: Dabei werden auch alle anderen Sitzungen abgemeldet."},
		Reset: {"Setze dein OpenPak-Passwort zurück",
			"Für dein OpenPak-Konto wurde das Zurücksetzen des Passworts angefordert.\n\nÖffne diesen Link, um ein neues Passwort zu wählen:\n{link}\n\nDer Link ist 24 Stunden gültig und kann einmal verwendet werden.\nWenn du das nicht angefordert hast, ignoriere diese Nachricht; dein Passwort bleibt unverändert."},
	},
	"fr": {
		Verify: {"Confirmez votre adresse e-mail OpenPak",
			"Bonjour{hi},\n\nConfirmez l'adresse e-mail de votre compte OpenPak\u00a0:\n{link}\n\nSi vous n'avez pas créé ce compte, ignorez ce message."},
		EmailChange: {"Confirmez votre nouvelle adresse e-mail OpenPak",
			"Bonjour{hi},\n\nOuvrez ce lien pour faire de cette adresse l'adresse e-mail de votre compte OpenPak\u00a0:\n{link}\n\nLe lien expire dans 24\u00a0heures et ne peut être utilisé qu'une fois. Si vous n'avez rien demandé, ignorez ce message."},
		EmailChangeNotice: {"L'adresse e-mail de votre compte OpenPak va changer",
			"Bonjour{hi},\n\nQuelqu'un connecté à votre compte OpenPak a demandé à le déplacer vers une nouvelle adresse e-mail. Rien ne change tant que le lien envoyé à cette adresse n'a pas été ouvert.\n\nSi ce n'était pas vous, changez votre mot de passe maintenant\u00a0: cela déconnecte aussi toutes les autres sessions."},
		Reset: {"Réinitialisez votre mot de passe OpenPak",
			"Une réinitialisation du mot de passe a été demandée pour votre compte OpenPak.\n\nOuvrez ce lien pour choisir un nouveau mot de passe\u00a0:\n{link}\n\nLe lien expire dans 24\u00a0heures et ne peut être utilisé qu'une fois.\nSi vous n'avez rien demandé, ignorez ce message\u00a0; votre mot de passe reste inchangé."},
	},
	"pt-BR": {
		Verify: {"Confirme seu e-mail no OpenPak",
			"Olá{hi},\n\nConfirme o endereço de e-mail da sua conta OpenPak:\n{link}\n\nSe você não criou esta conta, ignore esta mensagem."},
		EmailChange: {"Confirme seu novo endereço de e-mail no OpenPak",
			"Olá{hi},\n\nAbra este link para tornar este o endereço de e-mail da sua conta OpenPak:\n{link}\n\nO link expira em 24 horas e só pode ser usado uma vez. Se você não pediu isso, ignore esta mensagem."},
		EmailChangeNotice: {"O endereço de e-mail da sua conta OpenPak está sendo alterado",
			"Olá{hi},\n\nAlguém conectado à sua conta OpenPak pediu para mudá-la para um novo endereço de e-mail. Nada muda até que o link enviado para esse endereço seja aberto.\n\nSe não foi você, troque sua senha agora: isso também encerra todas as outras sessões."},
		Reset: {"Redefina sua senha do OpenPak",
			"Foi solicitada a redefinição da senha da sua conta OpenPak.\n\nAbra este link para escolher uma nova senha:\n{link}\n\nO link expira em 24 horas e só pode ser usado uma vez.\nSe você não pediu isso, ignore esta mensagem; sua senha continua a mesma."},
	},
	"it": {
		Verify: {"Conferma il tuo indirizzo email OpenPak",
			"Ciao{hi},\n\nconferma l'indirizzo email del tuo account OpenPak:\n{link}\n\nSe non hai creato tu questo account, ignora questo messaggio."},
		EmailChange: {"Conferma il tuo nuovo indirizzo email OpenPak",
			"Ciao{hi},\n\napri questo link per rendere questo l'indirizzo email del tuo account OpenPak:\n{link}\n\nIl link scade dopo 24 ore e può essere usato una sola volta. Se non l'hai richiesto, ignora questo messaggio."},
		EmailChangeNotice: {"L'indirizzo email del tuo account OpenPak sta cambiando",
			"Ciao{hi},\n\nqualcuno che ha effettuato l'accesso al tuo account OpenPak ha chiesto di spostarlo su un nuovo indirizzo email. Non cambia nulla finché non viene aperto il link inviato a quell'indirizzo.\n\nSe non sei stato tu, cambia subito la password: in questo modo vengono chiuse anche tutte le altre sessioni."},
		Reset: {"Reimposta la password di OpenPak",
			"È stata richiesta la reimpostazione della password del tuo account OpenPak.\n\nApri questo link per scegliere una nuova password:\n{link}\n\nIl link scade dopo 24 ore e può essere usato una sola volta.\nSe non l'hai richiesto, ignora questo messaggio; la tua password non cambia."},
	},
	"es": {
		Verify: {"Confirma tu correo electrónico de OpenPak",
			"Hola{hi}:\n\nConfirma el correo electrónico de tu cuenta de OpenPak:\n{link}\n\nSi no creaste esta cuenta, ignora este mensaje."},
		EmailChange: {"Confirma tu nuevo correo electrónico de OpenPak",
			"Hola{hi}:\n\nAbre este enlace para que esta sea la dirección de correo de tu cuenta de OpenPak:\n{link}\n\nEl enlace caduca en 24 horas y solo puede usarse una vez. Si no lo pediste, ignora este mensaje."},
		EmailChangeNotice: {"El correo electrónico de tu cuenta de OpenPak va a cambiar",
			"Hola{hi}:\n\nAlguien con sesión iniciada en tu cuenta de OpenPak pidió cambiarla a una nueva dirección de correo. No cambia nada hasta que se abra el enlace enviado a esa dirección.\n\nSi no fuiste tú, cambia tu contraseña ahora: así también se cierran todas las demás sesiones."},
		Reset: {"Restablece tu contraseña de OpenPak",
			"Se solicitó restablecer la contraseña de tu cuenta de OpenPak.\n\nAbre este enlace para elegir una nueva contraseña:\n{link}\n\nEl enlace caduca en 24 horas y solo puede usarse una vez.\nSi no lo pediste, ignora este mensaje; tu contraseña no cambia."},
	},
}

// Lang picks the first language in an Accept-Language header that has mails,
// by primary subtag (pt-PT reads pt-BR); English otherwise.
// ponytail: q-values ignored, list order taken as preference; the website sends one tag.
func Lang(acceptLanguage string) string {
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag, _, _ := strings.Cut(part, ";")
		base, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(tag)), "-")
		for l := range mails {
			if b, _, _ := strings.Cut(strings.ToLower(l), "-"); b == base {
				return l
			}
		}
	}
	return "en"
}

// Get returns a mail's subject and body in lang, with the greeting's name and
// the link filled in.
func Get(lang, kind, name, link string) (subject, body string) {
	m, ok := mails[lang][kind]
	if !ok {
		m = mails["en"][kind]
	}
	if name = strings.TrimSpace(name); name != "" {
		name = " " + name
	}
	return m.subject, strings.NewReplacer("{hi}", name, "{link}", link).Replace(m.body)
}
