package bot

import (
	"strings"
	"testing"
)

func TestRenderTasksArchivesOldDone(t *testing.T) {
	tasks := []taskItem{
		{Titulo: "pendiente", Completada: false},
		{Titulo: "hecha hoy", Completada: true, CompletadaEl: "2026-09-08"},
		{Titulo: "hecha ayer", Completada: true, CompletadaEl: "2026-09-07"},
		{Titulo: "hecha sin fecha", Completada: true},
	}
	out := renderTasks(tasks, "2026-09-08")

	if !strings.Contains(out, "⬜ pendiente") {
		t.Errorf("pending task missing:\n%s", out)
	}
	if !strings.Contains(out, "✅ hecha hoy") {
		t.Errorf("today's done task should be visible:\n%s", out)
	}
	if strings.Contains(out, "hecha ayer") || strings.Contains(out, "hecha sin fecha") {
		t.Errorf("archived tasks must be hidden:\n%s", out)
	}
	if !strings.Contains(out, "1 pendientes · 1 hechas hoy · 2 archivadas") {
		t.Errorf("footer counts wrong:\n%s", out)
	}
}

func TestRenderTasksAllArchived(t *testing.T) {
	tasks := []taskItem{{Titulo: "vieja", Completada: true, CompletadaEl: "2026-09-01"}}
	out := renderTasks(tasks, "2026-09-08")
	if strings.Contains(out, "vieja") || !strings.Contains(out, "1 archivadas") {
		t.Errorf("expected empty list with archived count:\n%s", out)
	}
}
