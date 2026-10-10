package bot

import "testing"

// Both directions: Spanish the gate MUST catch, and rafiña's real English
// (with B1 slips and stray Spanish words) it must NOT flag.
func TestLooksSpanish(t *testing.T) {
	spanish := []string{
		"Es la hora de mi cierre nocturno, pregúntame cómo estuvo mi día",
		"oye wey puedes revisar el disco de la vps porfa",
		"analiza, no cambies nada aún",
		"quiero que me mandes el video de las monarcas",
		"dale, hazlo y me avisas cuando termine",
		"no me gusta nada, intenta otra vez con otra fuente",
		"¿ya terminó la prueba de los modelos?",
		"mañana lo vemos con calma hermano",
	}
	english := []string{
		"hey man we need to free some storage please analize all vps",
		"I want to upload 1 per day man dont build none of media",
		"can you fix my teeth and remove papada",
		"I’m grateful for being surrounded by, it was almost all meetings",
		"How it’s going the behance tasks?",
		"Perfect let’s retake behance how we start with woods design project like the reference?",
		"what do you think about sonnet 5.5, it’s better than opus 5.0? Or not",
		"sorry not nu is plata card every first of each month",
		"I’m agradecido for the goof food",
		"Today was a good day, almost all was a meeting, we eat very delicious arepas",
	}
	short := []string{"dale", "ok thanks", "simón", "Woods", "sí hazlo"}

	for _, s := range spanish {
		if !looksSpanish(s) {
			t.Errorf("should be Spanish (gate must fire): %q", s)
		}
	}
	for _, s := range english {
		if looksSpanish(s) {
			t.Errorf("should be English (gate must NOT fire): %q", s)
		}
	}
	for _, s := range short {
		if looksSpanish(s) {
			t.Errorf("short message must pass through: %q", s)
		}
	}
}

// Commands and "!" always pass the quiz wall; nothing else does.
func TestQuizPasses(t *testing.T) {
	for _, s := range []string{"! esto es urgente, contéstame en español", "/gate off", "  /stop"} {
		if !quizPasses([]string{s}) {
			t.Errorf("must pass the wall: %q", s)
		}
	}
	for _, s := range []string{"oye wey cómo va todo", "ok", "what we have in r2?"} {
		if quizPasses([]string{s}) {
			t.Errorf("must not pass the wall: %q", s)
		}
	}
	if quizPasses(nil) {
		t.Error("an empty message must not pass")
	}
}
