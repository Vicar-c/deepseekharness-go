package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/Vicar-c/deepseekharness-go/internal/session"
)

// sessionRow is one line of the session list: the stored summary plus what the
// log says about the last finished turn.
type sessionRow struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Messages      int       `json:"messages"`
	Turns         int       `json:"turns"`
	Events        int       `json:"events"`
	LastTurn      string    `json:"lastTurn,omitempty"`
	LastErrorCode string    `json:"lastTurnErrorCode,omitempty"`
	Path          string    `json:"path,omitempty"`
	// Unreadable is set when the log could not be read; the summary is still
	// shown so the user can find and inspect the file.
	Unreadable string `json:"unreadable,omitempty"`
}

// runSessions lists stored sessions or shows one of them.
func runSessions(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	flags := addCommonFlags(fs)
	asJSON := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(fs.Args()) > 1 {
		fmt.Fprintf(stderr, "dsh: sessions takes at most one session id\n")
		return 2
	}
	env, err := flags.load(stderr)
	if err != nil {
		flags.reportFailure(stderr, err)
		return 1
	}
	defer env.closeLog()
	if dumped, code := dumpIfRequested(flags, env.cfg, env.tree, stdout, stderr); dumped {
		return code
	}

	store := session.NewStore(env.cfg.SessionDir)
	defer func() { _ = store.Close() }()

	if id := strings.TrimSpace(fs.Arg(0)); id != "" {
		row, err := readSessionRow(store, id)
		if err != nil {
			fmt.Fprintf(stderr, "dsh: %v\n", err)
			return 1
		}
		return printSession(stdout, stderr, row, *asJSON)
	}
	return printSessionList(stdout, stderr, store, *asJSON)
}

// readSessionRow loads the summary and the last turn outcome of one session.
func readSessionRow(store *session.Store, id string) (sessionRow, error) {
	loaded, err := store.Load(id)
	if err != nil {
		return sessionRow{}, err
	}
	row := sessionRow{
		ID:        loaded.ID,
		Title:     loaded.Title,
		CreatedAt: loaded.CreatedAt,
		UpdatedAt: loaded.UpdatedAt,
		Messages:  len(loaded.Messages),
		Turns:     loaded.Turn,
		Path:      filepath.Join(store.Dir(), loaded.ID+".jsonl"),
	}
	recording, err := store.Recording(id)
	if err != nil {
		row.Unreadable = err.Error()
		return row, nil
	}
	row.Events = len(recording.Events)
	applyLastTurn(&row, recording)
	return row, nil
}

// applyLastTurn reads the last turn/end reason from a recording.
func applyLastTurn(row *sessionRow, recording *session.Recording) {
	for index := len(recording.Events) - 1; index >= 0; index-- {
		event := recording.Events[index]
		if event.Type != "turn/end" {
			continue
		}
		var data session.TurnEndData
		if json.Unmarshal(event.Data, &data) != nil {
			return
		}
		row.LastTurn = data.Reason.Kind
		if data.Reason.Error != nil {
			row.LastErrorCode = data.Reason.Error.Code
		}
		return
	}
}

// printSessionList renders every stored session, newest first.
func printSessionList(stdout, stderr io.Writer, store *session.Store, asJSON bool) int {
	summaries, err := store.List()
	if err != nil {
		fmt.Fprintf(stderr, "dsh: %v\n", err)
		return 1
	}
	rows := make([]sessionRow, 0, len(summaries))
	for _, summary := range summaries {
		row, err := readSessionRow(store, summary.ID)
		if err != nil {
			// A summary came from the directory listing, so a read failure is
			// worth showing instead of dropping the session from the list.
			row = sessionRow{
				ID:         summary.ID,
				Title:      summary.Title,
				CreatedAt:  summary.CreatedAt,
				UpdatedAt:  summary.UpdatedAt,
				Messages:   summary.Messages,
				Path:       filepath.Join(store.Dir(), summary.ID+".jsonl"),
				Unreadable: err.Error(),
			}
			rows = append(rows, row)
			continue
		}
		rows = append(rows, row)
	}
	if asJSON {
		if err := writeJSON(stdout, rows); err != nil {
			fmt.Fprintf(stderr, "dsh: %v\n", err)
			return 1
		}
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(stdout, "no sessions")
		return 0
	}
	fmt.Fprintf(stdout, "%-28s  %-16s  %5s  %8s  %-16s  %s\n",
		"ID", "UPDATED", "TURNS", "MESSAGES", "LAST TURN", "TITLE")
	for _, row := range rows {
		fmt.Fprintf(stdout, "%-28s  %-16s  %5d  %8d  %-16s  %s\n",
			row.ID,
			row.UpdatedAt.Local().Format("2006-01-02 15:04"),
			row.Turns,
			row.Messages,
			lastTurnLabel(row),
			row.Title,
		)
	}
	return 0
}

// printSession prints one session's details.
func printSession(stdout, stderr io.Writer, row sessionRow, asJSON bool) int {
	if asJSON {
		if err := writeJSON(stdout, row); err != nil {
			fmt.Fprintf(stderr, "dsh: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "id: %s\n", row.ID)
	fmt.Fprintf(stdout, "title: %s\n", row.Title)
	fmt.Fprintf(stdout, "created: %s\n", row.CreatedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(stdout, "updated: %s\n", row.UpdatedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(stdout, "turns: %d\n", row.Turns)
	fmt.Fprintf(stdout, "messages: %d\n", row.Messages)
	fmt.Fprintf(stdout, "events: %d\n", row.Events)
	fmt.Fprintf(stdout, "last turn: %s\n", lastTurnLabel(row))
	fmt.Fprintf(stdout, "path: %s\n", row.Path)
	if row.Unreadable != "" {
		fmt.Fprintf(stdout, "unreadable: %s\n", row.Unreadable)
	}
	return 0
}

// lastTurnLabel renders the last turn outcome for a human reader.
func lastTurnLabel(row sessionRow) string {
	if row.Unreadable != "" {
		return "unreadable"
	}
	if row.LastTurn == "" {
		return "-"
	}
	if row.LastErrorCode != "" {
		return row.LastTurn + " [" + row.LastErrorCode + "]"
	}
	return row.LastTurn
}

// writeJSON prints one indented JSON document.
func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
