package copilot

import (
	"regexp"
	"strings"
	"unicode"
)

// Words only one of the two languages uses often: enough to tell which one a
// message is written in. Spanish and English are the languages Kobi's voice
// is written and reviewed in (prompts/kobi-identity.md).
var (
	spanishWords = map[string]bool{"el": true, "la": true, "los": true, "las": true, "de": true, "del": true, "que": true, "qué": true, "en": true, "y": true, "es": true, "por": true, "para": true, "con": true, "una": true, "un": true, "se": true, "no": true, "lo": true, "su": true, "al": true, "como": true, "cómo": true, "pero": true, "está": true, "están": true, "hay": true, "son": true, "mi": true, "tu": true, "me": true, "esto": true, "eso": true, "pasa": true, "pasó": true, "puedes": true, "puede": true, "quiero": true, "necesito": true, "dime": true, "revisa": true, "hazlo": true}
	englishWords = map[string]bool{"the": true, "and": true, "of": true, "to": true, "is": true, "in": true, "that": true, "for": true, "with": true, "on": true, "are": true, "this": true, "it": true, "was": true, "be": true, "by": true, "from": true, "not": true, "its": true, "has": true, "have": true, "which": true, "but": true, "or": true, "at": true, "what": true, "why": true, "how": true, "you": true, "my": true, "does": true, "did": true, "our": true, "your": true, "we": true, "please": true, "can": true, "could": true, "would": true, "should": true, "will": true, "an": true, "about": true, "there": true}
)

// DetectLanguage says whether a message is written in Spanish ("es") or
// English ("en"), or "" when it cannot tell. Two common words decide by
// majority; below that, one word of a language with no sign of the other
// (Spanish punctuation or accents) does, and anything else is left unsaid.
func DetectLanguage(text string) string {
	es, en := 0, 0
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if spanishWords[w] {
			es++
		}
		if englishWords[w] {
			en++
		}
	}
	spanishMarks := strings.ContainsAny(text, "¿¡ñÑáéíóúÁÉÍÓÚ")
	switch {
	case es+en >= 2 && es > en:
		return "es"
	case es+en >= 2 && en > es:
		return "en"
	case spanishMarks || es > 0 && en == 0:
		return "es"
	case en > 0 && es == 0:
		return "en"
	}
	return ""
}

// handoffPrefixRE is what a cluster switch puts before the carried question
// (web: services/copilot/handoff.ts): what Kobi established on the other
// side, in whatever language that Kobi wrote it. It is not the operator's.
var handoffPrefixRE = regexp.MustCompile(`^\((?:From cluster [^:\n]*|From the fleet view): [\s\S]*?\)\n\n`)

// OperatorLanguage is the language of the operator's latest message that
// shows one: a short "ok" after a Spanish question keeps the conversation in
// Spanish. A switched conversation's opening handoff note is left out: a
// Kobi that wrote it in English made the operator's Spanish question read as
// English (lab, 2026-10-10). So is the text the UI writes, while the operator
// wrote anything: the English follow-up of a scale that did not converge
// turned a Spanish conversation English (live test, 2026-10-10). Only a
// conversation the UI opened, where the operator has written nothing yet,
// goes by that text.
func OperatorLanguage(messages []Message) string {
	lang, operatorWrote := latestLanguage(messages, false)
	if lang != "" || operatorWrote {
		return lang
	}
	lang, _ = latestLanguage(messages, true)
	return lang
}

// latestLanguage reads the user messages from the newest back, the UI's own
// text only when uiText is set, and reports whether it read any.
func latestLanguage(messages []Message, uiText bool) (lang string, read bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != RoleUser || strings.TrimSpace(m.Content) == "" || writtenByUI(m) != uiText {
			continue
		}
		read = true
		if lang := DetectLanguage(handoffPrefixRE.ReplaceAllString(m.Content, "")); lang != "" {
			return lang, true
		}
	}
	return "", read
}

// writtenByUI says whether a user message is text the UI wrote (an Ask-Kobi
// button, Continue after the step limit, the follow-up of an action that did
// not converge). A cluster switch carries the operator's own question.
func writtenByUI(m Message) bool {
	return m.Trigger != "" && m.Trigger != "manual" && m.Trigger != "cluster_switch"
}

// OperatorLanguageBlock tells Kobi which language the operator writes in.
// The session's time zone is the operator's clock, not their language: with
// Europe/Madrid in the notes, Kobi answered questions written in English in
// Spanish (11 of 12 runs), and Haiku narrated its steps in
// English to an operator writing Spanish.
func OperatorLanguageBlock(lang string) string {
	name := map[string]string{"es": "Spanish", "en": "English"}[lang]
	if name == "" {
		return ""
	}
	return "# Operator language\nThe operator writes in " + name + ": write every message to them in " + name +
		", progress lines included. The time zone above is their clock, not their language."
}
