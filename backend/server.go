package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/pelletier/go-toml"
	"github.com/pkg/errors"
)

type formatType int

const (
	// FormatFC indicates that user requested FC report format output
	FormatFC = iota
	// FormatJSON indicates that user requested JSON report format output
	FormatJSON = iota
	// FormatText indicates that user requested text template report format output
	FormatText
)

func (d formatType) String() string {
	return [...]string{"FC", "JSON", "Text"}[d]
}

// TemplateString defines the template used to output a Report() with FormatText
var TemplateString = `{{define "Entry"}}
({{- .Duration}}) {{.Start.Hour}}:{{.Start.Minute}}-{{.Ts.Hour}}:{{.Ts.Minute}} -- {{.Title -}}
{{end}}

Report Start: {{.From}}
Report End: {{.To}}
Total Task Hours: {{.TaskHrs}}
Total Break Hours: {{.BrkHrs}}
Total Ignore Hours: {{.IgnoreHrs}}
{{$day := "" }}
{{range .Entries}}
{{- if ne $day .End.Weekday.String}}
{{$day = .End.Weekday.String}}

----------------------- {{$day}}, {{.End.Year}}-{{.End.Month}}-{{.End.Day}} -----------------------
{{end -}}
{{- template "Entry" .}}
{{- end -}}
`

// Backend represents the context and configuration of every instance of the omw command
// Immediate commands (like omw add, omw report), immediately affect the timesheet
// Long-running commands (like omw server), maintain a context
type Backend struct {
	ctx        context.Context
	config     *config
	fp         *os.File
	lastReport *Report
	worker     *worker
}

// ReportEntry describes a single entry in the timesheet
// Omw report and the REST API calculate some of the missing
// from the data stored on disk.
type ReportEntry struct {
	ID         string        `json:"id,omitempty"`
	Brk        bool          `json:"break,omitempty"`
	ClassNames []string      `json:"classNames,omitempty"`
	Duration   time.Duration `json:"duration,omitempty"`
	Ignore     bool          `json:"ignore,omitempty"`
	Start      time.Time     `json:"start,omitempty"`
	End        time.Time     `json:"end,omitempty"`
	Title      string        `json:"title,omitempty"`
	Ts         time.Time     `json:"timestamp,omitempty"`
	URL        string        `json:"url,omitempty"`
}

// SavedItems describes the structure of the entire TOML
// file.
type SavedItems struct {
	Entries []SavedEntry `toml:"entries"`
}

// SavedEntry describes the TOML format saved on disk
// for each entry.
// Note that the stored data is minimized to make it
// more suitable for human consumption
type SavedEntry struct {
	ID   string    `toml:"id"`
	End  time.Time `toml:"end"`
	Task string    `toml:"task"`
}

// FCReport describes the format of a FullCalendar-compatible report
type FCReport struct {
	Events []ReportEntry `json:"events"`
}

// Report describes a report
// previous is only used during report calculation to
// populate ReportEntry.Duration
type Report struct {
	From      time.Time     `json:"reportFrom"`
	To        time.Time     `json:"reportTo"`
	IgnoreHrs time.Duration `json:"ignoreTotalHours"`
	BrkHrs    time.Duration `json:"breakTotalHours"`
	TaskHrs   time.Duration `json:"taskTotalHours"`
	Entries   []ReportEntry `json:"entries"`
	previous  *time.Time
}

type config struct {
	omwDir  string
	omwFile string
	omwTerm string
}

type worker struct {
	cmd            string
	leftShiftDown  bool
	rightShiftDown bool
}

// Add appends the current time and task to your timesheet
func (b *Backend) Add(args []string) error {
	task := strings.Join(args, " ")
	return b.addEntry(task)
}

// Close cleans up before exiting
func (b *Backend) Close() error {
	if b.fp != nil {
		b.fp.Close()
	}
	return nil
}

// Edit opens your current timesheet in your default editor or
// in the editor specified by the EDITOR environment variable
// Similar to visudo, will do some basic checks to ensure
// that any edits will still pass toml.Marshal() and that there
// are no duplicate IDs
// should return true, err to ask the caller to re-run Edit()
func (b *Backend) Edit() (bool, error) {
	editor := DefaultEditor
	fileLock := flock.New(b.config.omwFile)
	term := DefaultTerm

	locked, err := fileLock.TryLock()
	defer fileLock.Unlock()
	if err != nil {
		return false, err
	}
	if !locked {
		return false, errors.New("unable to get file lock")
	}

	// copy file
	source, err := os.Open(b.config.omwFile)
	if err != nil {
		return false, err
	}
	defer source.Close()
	pat := fmt.Sprintf("%s*", filepath.Base(b.config.omwFile))
	tmpFile, err := ioutil.TempFile(filepath.Dir(b.config.omwFile), pat)
	defer tmpFile.Close()
	if err != nil {
		return false, err
	}
	_, err = io.Copy(tmpFile, source)
	if err != nil {
		return false, err
	}

	if preferredEditor := os.Getenv("EDITOR"); preferredEditor != "" {
		editor = preferredEditor
	}
	runCmd := editor
	if preferredTerm := os.Getenv("OMW_TERM"); runtime.GOOS != "windows" && preferredTerm != "" {
		term = preferredTerm
		runCmd = fmt.Sprintf("%s -e %s", term, editor)
	}

	tmpPath := tmpFile.Name()
	argv := []string{tmpPath}
	cmd := exec.CommandContext(b.ctx, runCmd, argv...)
	// should work if run from terminal
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	err = runCommand(cmd)
	if err != nil {
		tmpFile.Close()
		inner := os.Remove(tmpPath)
		return false, errors.Wrap(err, inner.Error())
	}

	// after edits, lock tmpFile and validate changes
	tmpLock := flock.New(tmpPath)
	tmpLocked, err := tmpLock.TryLock()
	defer tmpLock.Unlock()
	if err != nil {
		tmpFile.Close()
		inner := os.Remove(tmpPath)
		return false, errors.Wrap(err, inner.Error())
	}
	if !tmpLocked {
		tmpFile.Close()
		err = errors.New("unable to get file lock on tmpFile")
		inner := os.Remove(tmpPath)
		return false, errors.Wrap(err, inner.Error())
	}

	validated, err := validateEdit(tmpFile.Name())
	if err != nil {
		tmpFile.Close()
		inner := os.Remove(tmpPath)
		innerErr := ""
		if inner != nil {
			innerErr = inner.Error()
		}
		return true, errors.Wrap(err, innerErr)
	}
	if len(validated.Entries) == 0 {
		return false, errors.Wrapf(err, "got zero entries from edit - manually remove %s to clear all tasks", b.config.omwFile)
	}
	validatedBytes, err := toml.Marshal(validated)
	if err != nil {
		return false, errors.Wrap(err, "can't marshal data in edit")
	}

	// backup current file before overwriting
	input, err := ioutil.ReadFile(b.config.omwFile)
	if err != nil {
		return false, errors.Wrap(err, "reading backup file")
	}
	backup := fmt.Sprintf("%s.bak", b.config.omwFile)
	err = ioutil.WriteFile(backup, input, 0644)
	if err != nil {
		return false, errors.Wrap(err, "writing backup file")
	}

	err = ioutil.WriteFile(tmpFile.Name(), validatedBytes, 0644)
	if err != nil {
		return false, errors.Wrap(err, "saving new data")
	}
	os.Rename(tmpPath, b.config.omwFile)
	return false, err
}

// Hello appends a newline and then another line to end of timesheet with current time
// and the word "Hello".  Meant to be run at the beginning of a new work day
func (b *Backend) Hello() error {
	return b.addEntry("hello")
}

// Report outputs various report formats to one of the following types:
// Text - command-line default
// JSON - web default
// FC   - web fullcalendar JSON feed URL
// Add 24 hours to the parsed end time so that when a user specifies
// --from 2019-01-01 --to 2019-01-02
// that translates to "report on tasks that occurred between 2019-01-01 00:00
// and "2019-01-03 00:00"
func (b *Backend) Report(start, end string, format string) (output string, err error) {
	fcLayout := "2006-01-02T15:04:05-07:00"
	layout := "2006-1-2" // should support optional leading zeros
	//layoutEvent := "2006-1-2 15:4"
	report := Report{}
	loc := time.Now().Location()
	report.From, err = time.ParseInLocation(layout, start, loc)
	if err != nil {
		report.From, err = time.ParseInLocation(fcLayout, start, loc)
	}
	if err != nil {
		return "", errors.Wrap(err, "can't parse report start time")
	}

	report.To, err = time.ParseInLocation(layout, end, loc)
	if err != nil {
		report.To, err = time.ParseInLocation(fcLayout, end, loc)
	}
	if err != nil {
		return "", errors.Wrap(err, "can't parse report end time")
	}
	report.To = report.To.Add(24 * time.Hour)
	r, err := ioutil.ReadFile(b.config.omwFile)
	if err != nil {
		return "", errors.Wrap(err, "can't read data file for report")
	}
	data := SavedItems{}
	err = toml.Unmarshal(r, &data)
	if err != nil {
		return "", errors.Wrap(err, "can't unmarshal data")
	}

	for _, e := range data.Entries {
		// Indicates line is missing required information
		if e.Task == "" {
			continue
		}

		// Indicates task timestamp is outside the requested time period
		if e.End.Before(report.From) || e.End.After(report.To) {
			continue
		}
		entry, err := b.parseEntry(e.Task)
		if err != nil {
			continue
		}
		entry.Ts = e.End
		if err != nil {
			continue
		}
		// Should indicate first task in requested report time period
		if report.previous == nil {
			report.previous = &entry.Ts
			entry.End = entry.Ts
			report.Entries = append(report.Entries, *entry)
			continue
		}
		// For now, we explicitly assume that a new day restarts the duration calculation
		// We may change the marker from new day to first entry of "hello" on a given day
		// to better allow tracking tasks that extend from a previous day into a new day
		if entry.Ts.Day() != (*report.previous).Day() {
			report.previous = &entry.Ts
			entry.End = entry.Ts
		}
		entry.End = *report.previous
		entry.Duration = entry.Ts.Sub(*report.previous)

		*report.previous = entry.Ts
		// Use else if to make it clear we only process the event's
		// duration one time
		if entry.Ignore == false && entry.Brk == false {
			report.TaskHrs += entry.Duration
		} else if entry.Ignore == true && entry.Brk == false {
			report.IgnoreHrs += entry.Duration
		} else if entry.Ignore == false && entry.Brk == true {
			report.BrkHrs += entry.Duration
		} else if entry.Ignore == true && entry.Brk == true {
			return "", errors.New("entry has both break and ignore set to true")
		}
		report.Entries = append(report.Entries, *entry)

	}
	f := FormatText
	if format == "json" {
		f = FormatJSON
	}
	if format == "fc" {
		f = FormatFC
	}
	b.lastReport = &report
	output, err = b.formatReport(report, formatType(f))
	if err != nil {
		return "", err
	}
	return output, nil
}

// Stretch append current timestamp to end of timesheet and copy previous task
// fp is opened in append mode, so seek to beginning of file first
func (b *Backend) Stretch() error {
	r, err := ioutil.ReadFile(b.config.omwFile)
	if err != nil {
		return err
	}
	data := SavedItems{}
	err = toml.Unmarshal(r, &data)
	if err != nil {
		return err
	}

	lastEntry := data.Entries[len(data.Entries)-1]
	if lastEntry.Task == "" {
		return errors.New("missing task description for stretch")
	}
	err = b.addEntry(lastEntry.Task)
	if err != nil {
		return err
	}
	return nil
}

// addEntry seeks to end of file and appends a formatted string
// will create a new empty file if file is missing
func (b *Backend) addEntry(s string) error {
	fp, err := os.OpenFile(b.config.omwFile, os.O_APPEND|os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return errors.Wrapf(err, "can't open or create %s: %q", b.config.omwFile, err)
	}
	defer fp.Close()
	data := SavedItems{}
	entry := SavedEntry{}
	entry.ID = uuid.New().String()
	entry.End = time.Now()
	entry.Task = s
	data.Entries = append(data.Entries, entry)
	entriesBytes, err := toml.Marshal(data)
	if err != nil {
		return errors.Wrap(err, "can't marshal data")
	}
	toSave := string(entriesBytes)
	fileLock := flock.New(b.config.omwFile)
	locked, err := fileLock.TryLock()
	defer fileLock.Unlock()
	if err != nil {
		return errors.Wrap(err, "unable to get file lock")
	}
	if !locked {
		return errors.New("unable to get file lock")
	}
	_, err = fp.WriteString(toSave)
	if err != nil {
		return errors.Wrap(err, "error saving new data")
	}
	return nil
}

func (b *Backend) formatReport(report Report, format formatType) (string, error) {
	if format == FormatJSON {
		output, err := json.Marshal(report)
		return string(output), err
	}

	entries := []ReportEntry{}
	if format == FormatFC {
		for _, entry := range report.Entries {
			classes := []string{}
			if entry.Brk {
				classes = append(classes, "breakEntry")
			}
			if entry.Ignore {
				classes = append(classes, "ignoreEntry")
			}

			entries = append(entries, ReportEntry{
				Start:      entry.Start,
				End:        entry.Start.Add(entry.Duration),
				Title:      entry.Title,
				URL:        "",
				ClassNames: classes,
			})
		}
		data := FCReport{
			Events: entries,
		}
		output, err := json.Marshal(data.Events)
		return string(output), err
	}

	// fallback to text format
	reportTmpl, err := template.New("report").Parse(TemplateString)
	if err != nil {
		return "", err
	}
	err = reportTmpl.Execute(os.Stdout, report)
	if err != nil {
		panic(err)
	}
	return "", nil
}

func (b *Backend) parseEntry(s string) (*ReportEntry, error) {
	re := regexp.MustCompile(`(?P<task>[a-zA-Z0-9,._+:@%\/-]+[a-zA-Z0-9,._+:@%\/\-\t ]*) ?(?P<mod>\*\*\*?)*`)
	matches := re.FindStringSubmatch(s)
	if matches == nil {
		return nil, errors.New("invalid string")
	}
	entry := &ReportEntry{
		Title: matches[1],
	}
	if matches[2] == "**" {
		entry.Brk = true
	}
	if matches[2] == "***" {
		entry.Ignore = true
	}
	return entry, nil
}

// Create an instance of the structures that operate on Omw data
func Create(fp *os.File, omwDir, omwFile string) *Backend {
	return &Backend{
		ctx: context.Background(),
		config: &config{
			omwDir:  omwDir,
			omwFile: omwFile,
		},
		fp:     fp,
		worker: nil,
	}
}

// runCommand Executes cmd and handles any output
func runCommand(cmd *exec.Cmd) error {
	err := cmd.Run()
	if err != nil {
		log.Println(err)
		return err
	}
	return nil
}

// validateEdit ensures that f:
// 1. Can be successfully unmarshaled into an OMW data structure
// 2. Has no duplicate IDs
// 3. If it finds a duplicate ID, attempt to auto-correct without prompting
// We don't use the IDs in the CLI for now.
//
// It does not:
// 1. Check for in-order task times
func validateEdit(fn string) (*SavedItems, error) {
	keys := make(map[string]bool)
	data := SavedItems{}
	r, err := ioutil.ReadFile(fn)
	if err != nil {
		return nil, errors.Wrap(err, "reading temporary file")
	}
	err = toml.Unmarshal(r, &data)
	if err != nil {
		return nil, errors.Wrap(err, "TOML formatting error please try again")
	}

	for i, e := range data.Entries {
		if _, exists := keys[e.ID]; exists {
			log.Printf("Duplicate ID found - %s - fixing", e.ID)
			newID := uuid.New().String()
			log.Printf("New ID = %s", newID)
			keys[e.ID] = true
			data.Entries[i].ID = newID
			continue
		}
		keys[e.ID] = false
	}
	return &data, nil
}
