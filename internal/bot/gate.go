package bot

import (
	"strings"
	"unicode"
)

// Language detection for the English gate (/gate on). The gate itself — the
// quiz wall — lives in quiz.go; this file only tells Spanish from English, so a
// Spanish message gets a "Say it in English" quiz instead of a grammar one.
//
// The first gate (2-oct-2026) bounced Spanish with its English version and a
// Gemma 4 coach bounced typed English with a major mistake (13b6289, 077e226,
// faa6504's praise). rafiña had it removed on 7/8-oct ("delete the blocker") and
// asked for the quiz wall instead on 9-oct.

const gateMinWords = 3

// Spanish-only function words (no English homographs like "no", "a", "me", "is").
var esWords = map[string]bool{
	"que": true, "qué": true, "de": true, "del": true, "la": true, "las": true, "el": true, "los": true,
	"por": true, "para": true, "con": true, "una": true, "uno": true, "unos": true, "pero": true,
	"como": true, "cómo": true, "más": true, "porque": true, "también": true, "ya": true, "eso": true,
	"esto": true, "esta": true, "este": true, "está": true, "estoy": true, "hay": true, "muy": true,
	"mi": true, "tu": true, "su": true, "se": true, "lo": true, "le": true, "nos": true, "sí": true,
	"y": true, "o": true, "en": true, "es": true, "son": true, "hola": true, "gracias": true,
	"puedes": true, "quiero": true, "tengo": true, "hacer": true, "hace": true, "ahorita": true,
	"wey": true, "güey": true, "hermano": true, "oye": true, "pues": true, "bien": true, "cuando": true,
	"dónde": true, "donde": true, "cuál": true, "cual": true, "todo": true, "nada": true, "algo": true,
	"mañana": true, "hoy": true, "ayer": true, "ahora": true, "aquí": true, "favor": true, "neta": true,
	"ver": true, "dime": true, "hazlo": true, "cuánto": true, "cuanto": true, "sus": true, "al": true,
}

// English function words that rarely appear in Spanish text.
var enWords = map[string]bool{
	"the": true, "and": true, "is": true, "are": true, "you": true, "i": true, "to": true, "of": true,
	"it": true, "that": true, "this": true, "what": true, "how": true, "can": true, "please": true,
	"my": true, "me": true, "do": true, "have": true, "with": true, "for": true, "on": true, "in": true,
	"we": true, "be": true, "was": true, "will": true, "would": true, "should": true, "your": true,
	"i'm": true, "it's": true, "don't": true, "want": true, "need": true, "about": true, "if": true,
	"so": true, "but": true, "or": true, "not": true, "they": true, "there": true, "where": true,
	"when": true, "why": true, "which": true, "from": true, "at": true, "an": true, "a": true,
	"just": true, "now": true, "today": true, "thanks": true, "let's": true, "man": true, "bro": true,
}

// gateWords splits a message into lowercase words, keeping accents and apostrophes.
func gateWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(unicode.IsLetter(r) || r == '\'' || r == '’')
	})
}

// looksSpanish reports whether a message is mostly Spanish. A word scores for a
// language when it's a function word of that language; Spanish-only characters
// (ñ, ¿, ¡, accented vowels) add weight. English with a stray Spanish word
// ("remove papada") stays English.
func looksSpanish(s string) bool {
	words := gateWords(s)
	if len(words) < gateMinWords {
		return false
	}
	es, en := 0, 0
	for _, w := range words {
		w = strings.ReplaceAll(w, "’", "'")
		if esWords[w] {
			es++
		}
		if enWords[w] {
			en++
		}
	}
	for _, r := range strings.ToLower(s) {
		switch r {
		case 'ñ', '¿', '¡', 'á', 'é', 'í', 'ó', 'ú':
			es++
		}
	}
	return es >= 2 && es > en
}
