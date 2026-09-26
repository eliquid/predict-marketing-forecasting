package main

// Talking to a model.
//
// A model is a Python file. Go starts it, reads one handshake line describing
// what it is and what it can do, then exchanges one JSON line per forecast.
//
// This file knows nothing about TimesFM or Chronos specifically. Everything
// model-shaped comes from the handshake, so adding a model is a new .py file
// and a line in `models` below -- no change to any Go file.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// The only place in the Go code that knows a model exists.
var models = map[string]string{
	"timesfm3":   "models/timesfm3_worker.py",
	"chronos2":   "models/chronos2_worker.py",
	"chronos2ft": "models/chronos2ft_worker.py", // Chronos-2 + a LoRA adapter trained on your data
}

func modelNames() []string {
	out := make([]string, 0, len(models))
	for k := range models {
		out = append(out, k)
	}
	return out
}

// Handshake is the worker's first line: what it is, and what it can do.
// Stored verbatim on every run so any forecast can name what produced it.
type Handshake struct {
	Model         string            `json:"model"`
	Repo          string            `json:"repo"`
	Revision      string            `json:"revision"`
	WeightsSHA256 string            `json:"weights_sha256"`
	Covariates    bool              `json:"covariates"` // accepts known-future values?
	Quantiles     []float64         `json:"quantiles"`
	Versions      map[string]string `json:"versions"`
	// Set only by a model fitted to the user's own data.
	TrainedThrough string `json:"trained_through"`
	TrainedOn      string `json:"trained_on"`
}

type request struct {
	ID string `json:"id"`
	// Series is [metric][day]. Both models are multivariate and forecast every
	// row in one call, sharing what they learn across them -- spend, impressions
	// and clicks move together, and the models are built to use that.
	Series    [][]float64 `json:"series"`
	Metrics   []string    `json:"metrics"`
	Horizon   int         `json:"horizon"`
	Quantiles []float64   `json:"quantiles"`
	// Covariates always travel as a pair: the real history from the data, and the
	// known future. A model may not be given one without the other -- inventing a
	// history to satisfy the API feeds the model noise and it shows up as a wider
	// band rather than an error.
	PastCovariates   map[string][]float64 `json:"past_covariates,omitempty"`
	FutureCovariates map[string][]float64 `json:"future_covariates,omitempty"`
}

type response struct {
	ID        string        `json:"id"`
	Quantiles [][][]float64 `json:"quantiles"` // [metric][day][quantile]
	Error     string        `json:"error"`
}

// lastLine returns the final message a worker printed before giving up.
//
// Workers explain themselves over several lines, with continuations indented, so
// this walks back to the last unindented line and takes everything from there --
// otherwise you get the "run this command" tail without the sentence saying why.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			continue
		}
		start = i
		if !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			break
		}
	}
	if start < 0 {
		return ""
	}
	var parts []string
	for _, l := range lines[start:] {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

// forecastTimeout is when we stop waiting for an answer.
//
// Measured on this machine: a forecast takes ~3 seconds whether the horizon is 7
// days or 400, because nearly all of it is loading the model. Anything still
// running after five minutes is stuck, not busy.
const forecastTimeout = 5 * time.Minute

// startupTimeout bounds the handshake. Loading weights off a cold disk is slow and
// legitimate, so this is generous; it exists to end an infinite wait, not to hurry
// anyone. Lower than forecastTimeout because starting up does less work than
// forecasting does.
const startupTimeout = 3 * time.Minute

// Worker is a running model process.
type Worker struct {
	Name    string
	Shake   Handshake
	Raw     json.RawMessage // handshake exactly as received
	Timeout time.Duration   // zero means forecastTimeout

	// LastCrossing is how far the last forecast's quantiles were out of order
	// before being sorted, as a fraction. Reported so the repair is never silent.
	LastCrossing float64
	// LastClamped counts quantile values the last forecast put below zero, which
	// floorAtZero raised to zero. Announced for the same reason the crossing repair
	// is: a run of exactly 0.0 across several quantiles is the clamp, not the
	// model, and a page that prints "low 0" for a dying campaign otherwise looks
	// the same as one where the model is merely unsure.
	LastClamped int

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	in     *bufio.Writer
	out    *bufio.Scanner
	n      int
	killed bool // timed out; the stream is desynchronised and the process is gone
}

// installDir is where models/ lives: next to the binary, or the current
// directory when running from a source checkout.
//
// Resolving these relative to the working directory meant the tool only worked
// while you were standing in the project folder -- run it on a CSV anywhere else
// and it could not find its own models.
func installDir() string {
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			d := filepath.Dir(exe)
			if _, err := os.Stat(filepath.Join(d, "models")); err == nil {
				return d
			}
		}
	}
	if _, err := os.Stat("models"); err == nil {
		return "."
	}
	return ""
}

// venvPython locates the interpreter inside models/.venv.
//
// uv lays the environment out differently per platform: bin/python everywhere
// except Windows, which uses Scripts/python.exe. Only the POSIX path was ever
// checked, so the Windows instructions in the README produced an environment the
// program then said did not exist. The POSIX path is returned when neither is
// present, because that is what the error message should name on the platforms
// almost everyone is using.
func venvPython(root string) string {
	posix := filepath.Join(root, "models", ".venv", "bin", "python")
	windows := filepath.Join(root, "models", ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(posix); err == nil {
		return posix
	}
	if _, err := os.Stat(windows); err == nil {
		return windows
	}
	return posix
}

func startWorker(name string) (*Worker, error) {
	script, ok := models[name]
	if !ok {
		return nil, fmt.Errorf("unknown model %q (have: %s)", name, strings.Join(modelNames(), ", "))
	}
	root := installDir()
	if !filepath.IsAbs(script) {
		if root == "" {
			return nil, fmt.Errorf("cannot find the models folder next to the binary " +
				"or in the current directory")
		}
		script = filepath.Join(root, script)
	}
	if _, err := os.Stat(script); err != nil {
		return nil, fmt.Errorf("model %q: %s not found", name, script)
	}
	python := venvPython(root)
	if _, err := os.Stat(python); err != nil {
		// ./install.sh, not `setup`: setup downloads weights into an environment
		// that has to exist already, so on a fresh clone it only produces a second
		// error telling you to build the environment by hand.
		return nil, fmt.Errorf("no Python environment at %s -- run ./install.sh first, "+
			"which builds it and downloads the model weights", python)
	}

	cmd := exec.Command(python, script)
	// Keep a copy of what the worker writes to stderr as well as passing it
	// through. When a model fails to start, its own explanation is far more use
	// than "exited before saying hello".
	var errLog strings.Builder
	cmd.Stderr = io.MultiWriter(os.Stderr, &errLog)
	// Its own process group, so killGroup can reach the helpers torch and OpenMP
	// start. Killing only the direct child leaves those holding the stderr pipe,
	// which is what made cmd.Wait hang after the work was already finished.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Do not litter the models folder with __pycache__ for two ~70-line scripts.
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", script, err)
	}

	w := &Worker{Name: name, cmd: cmd, stdin: stdin, in: bufio.NewWriter(stdin),
		Timeout: forecastTimeout}
	w.out = bufio.NewScanner(stdout)
	w.out.Buffer(make([]byte, 0, 1<<20), 64<<20) // forecasts can be long lines

	// Bounded, like every other read from a model. It was a bare Scan, so a worker
	// stuck loading -- a stalled cache mount, a sha256 re-read on a hung volume --
	// hung the whole command forever with no output at all, not even a line saying
	// which model it was waiting for. An import runs six of these, and a scheduled
	// one would simply never return.
	hello := make(chan bool, 1)
	go func() { hello <- w.out.Scan() }()
	select {
	case ok := <-hello:
		if !ok {
			cmd.Wait()
			if why := lastLine(errLog.String()); why != "" {
				return nil, fmt.Errorf("model %q could not start: %s", name, why)
			}
			return nil, fmt.Errorf("model %q exited before saying hello (see errors above)", name)
		}
	case <-time.After(startupTimeout):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("model %q did not say hello within %s. It is stuck "+
			"starting up -- usually loading weights from a slow or stalled disk, or "+
			"re-reading them to check their checksum. %s",
			name, startupTimeout, lastLine(errLog.String()))
	}
	w.Raw = json.RawMessage(append([]byte(nil), w.out.Bytes()...))
	if err := json.Unmarshal(w.Raw, &w.Shake); err != nil {
		return nil, fmt.Errorf("model %q sent a bad handshake: %w", name, err)
	}
	return w, nil
}

// Close lets the model shut itself down. Closing stdin ends its read loop, which
// gives torch a chance to release its shared memory; killing the process instead
// leaves semaphores behind and prints warnings on the way out.
func (w *Worker) Close() error {
	done := make(chan error, 1)
	go func() { done <- w.cmd.Wait() }()
	if w.killed {
		return w.reap(done, "") // already killed by a timeout; just reap it
	}
	w.in.Flush()
	w.stdin.Close()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		killGroup(w.cmd)
		return w.reap(done, fmt.Sprintf("model %q did not exit; killed", w.Name))
	}
}

// reap waits for the process, but not forever.
//
// cmd.Wait cannot return while ANY descendant still holds the stderr pipe it
// inherited, and Kill only kills the direct child -- so a torch or OpenMP helper
// outliving its parent wedged the whole command after the report had already been
// written: work finished, file on disk, process never exits. Every exit path runs
// this defer, so a scheduled job hung with nothing wrong and nothing to show.
func (w *Worker) reap(done chan error, msg string) error {
	select {
	case err := <-done:
		if msg != "" {
			return errors.New(msg)
		}
		return err
	case <-time.After(5 * time.Second):
		// A descendant is holding the pipe open. The child is dead and everything
		// is written; leaving is correct, and saying nothing about it is not.
		if msg == "" {
			msg = fmt.Sprintf("model %q left a background process holding its output "+
				"open; carrying on without waiting for it", w.Name)
		}
		return errors.New(msg)
	}
}

// killGroup kills the worker and anything it started. Kill on its own leaves the
// grandchildren that are the actual problem.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if pgid, err := syscall.Getpgid(cmd.Process.Pid); err == nil && pgid == cmd.Process.Pid {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}

// Forecast sends one request and validates the answer before returning it.
func (w *Worker) Forecast(series [][]float64, metrics []string, horizon int,
	quantiles []float64, past, future map[string][]float64) ([][][]float64, error) {

	if len(series) == 0 {
		return nil, fmt.Errorf("nothing to forecast")
	}
	if len(series) != len(metrics) {
		return nil, fmt.Errorf("%d series but %d metric names", len(series), len(metrics))
	}
	days := len(series[0])
	for i, s := range series {
		if len(s) != days {
			return nil, fmt.Errorf("metric %q has %d days but %q has %d",
				metrics[i], len(s), metrics[0], days)
		}
	}

	if len(future) > 0 && !w.Shake.Covariates {
		return nil, fmt.Errorf("model %q cannot use known-future values, and %d were given. "+
			"Use a model that supports them, or drop them -- they will not be silently ignored",
			w.Name, len(future))
	}
	for name, v := range future {
		if len(v) != horizon {
			return nil, fmt.Errorf("known-future %q has %d values but the horizon is %d",
				name, len(v), horizon)
		}
		p, ok := past[name]
		if !ok {
			return nil, fmt.Errorf("known-future %q has no history. "+
				"It must be a column in your CSV so the model can see how it behaved before", name)
		}
		if len(p) != days {
			return nil, fmt.Errorf("history of %q has %d values but the series has %d",
				name, len(p), days)
		}
	}
	// Only send the history of covariates whose future is actually given: Chronos
	// requires future keys to be a subset of past keys, and a past-only column
	// would otherwise be passed with no future and rejected by the model.
	sendPast := map[string][]float64{}
	for name := range future {
		sendPast[name] = past[name]
	}

	w.n++
	req := request{ID: fmt.Sprint(w.n), Series: series, Metrics: metrics, Horizon: horizon,
		Quantiles: quantiles, PastCovariates: sendPast, FutureCovariates: future}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := w.in.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	if err := w.in.Flush(); err != nil {
		return nil, err
	}

	reply, err := w.readLine()
	if err != nil {
		return nil, err
	}
	var resp response
	if err := json.Unmarshal(reply, &resp); err != nil {
		return nil, fmt.Errorf("model %q sent unreadable output: %w", w.Name, err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("model %q: %s", w.Name, resp.Error)
	}
	if resp.ID != req.ID {
		return nil, fmt.Errorf("model %q answered request %s with %s", w.Name, req.ID, resp.ID)
	}
	if len(resp.Quantiles) != len(metrics) {
		return nil, fmt.Errorf("model %q returned %d metrics, expected %d",
			w.Name, len(resp.Quantiles), len(metrics))
	}
	w.LastCrossing, w.LastClamped = 0, 0
	for m, per := range resp.Quantiles {
		crossing, err := checkForecast(per, horizon, len(quantiles))
		if err != nil {
			return nil, fmt.Errorf("model %q returned an unusable forecast for %q: %w",
				w.Name, metrics[m], err)
		}
		if crossing > w.LastCrossing {
			w.LastCrossing = crossing
		}
		w.LastClamped += floorAtZero(metrics[m], per)
	}
	return resp.Quantiles, nil
}

// readLine waits for one reply, giving up after Timeout.
//
// On a timeout the model is killed: it may still be mid-answer, and a late reply
// would be read as the response to the NEXT request, attaching one series'
// forecast to another.
func (w *Worker) readLine() ([]byte, error) {
	type result struct {
		line []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		if w.out.Scan() {
			ch <- result{line: append([]byte(nil), w.out.Bytes()...)}
			return
		}
		err := w.out.Err()
		if err == nil {
			err = errors.New("stopped responding (see errors above)")
		}
		ch <- result{err: err}
	}()

	timeout := w.Timeout
	if timeout <= 0 {
		timeout = forecastTimeout
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("model %q: %w", w.Name, r.err)
		}
		return r.line, nil
	case <-time.After(timeout):
		w.killed = true
		_ = w.cmd.Process.Kill()
		return nil, fmt.Errorf("model %q did not answer within %s, so it was stopped. "+
			"A forecast normally takes a few seconds whatever the horizon, so this means "+
			"it is stuck rather than busy", w.Name, timeout)
	}
}

// absurdCrossing is where a crossing stops looking like a model artefact and
// starts looking like a broken forecast. Observed crossings are ~0.2%.
const absurdCrossing = 0.05

// checkForecast is the contract every model's output must satisfy before it is
// stored, and it repairs the one defect that is expected rather than wrong.
//
// Both models predict each quantile independently, so nothing forces q30 <= q40.
// Small crossings are a normal artefact -- 0.17% was observed on a real series --
// and the standard remedy is monotonic rearrangement: sort each day's quantiles.
// Sorting a crossed quantile curve is a valid and strictly better estimate
// (Chernozhukov et al., "Quantile and Probability Curves Without Crossing").
//
// So crossings are corrected rather than rejected, and the size of the largest
// correction is returned so the caller can say it happened. A crossing beyond
// absurdCrossing is not an artefact and is still refused.
// floorAtZero clamps a forecast that cannot be negative.
//
// You cannot spend minus three pounds, or serve minus one impression. A campaign
// that runs Monday to Friday and stops at the weekend made chronos2 return a
// median of -2.891 and a q10 of -23.720 for Saturday: the weekly shape was read
// correctly, and then extrapolated through the floor. Printing a lower estimate of
// -£23.72 of spend to a client is a credibility problem, and the negative flows on
// into the account total and into whatever accuracy scores it later.
//
// Only the concepts that are physically non-negative. Revenue is deliberately not
// one of them: a refund is a real negative. Clamping preserves the ordering
// checkForecast has just verified, since max(0,x) is monotonic.
func floorAtZero(metric string, days [][]float64) int {
	switch c, _, ok := metricConcept(metric); {
	case !ok, c == "revenue", c == "rate", c == "ratio":
		return 0
	case c == "spend", c == "impressions", c == "clicks", c == "conversions",
		c == "cost per":
	default:
		return 0
	}
	n := 0
	for _, day := range days {
		for j, v := range day {
			if v < 0 {
				day[j] = 0
				n++
			}
		}
	}
	return n
}

func checkForecast(q [][]float64, horizon, nq int) (float64, error) {
	if len(q) != horizon {
		return 0, fmt.Errorf("got %d days, expected %d", len(q), horizon)
	}
	// Judge a crossing against the size of the whole forecast, not against the two
	// values that crossed. A series that is flat at zero comes back as float noise
	// around 1e-8, where two adjacent values can "cross by 10%" while differing by
	// a billionth -- which is not a defect, it is the absence of a signal.
	scale := 0.0
	for _, day := range q {
		for _, v := range day {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			scale = math.Max(scale, math.Abs(v))
		}
	}
	negligible := scale < 1e-6

	worst := 0.0
	for i, day := range q {
		if len(day) != nq {
			return 0, fmt.Errorf("day %d has %d quantiles, expected %d", i, len(day), nq)
		}
		for j, v := range day {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return 0, fmt.Errorf("day %d quantile %d is %v", i, j, v)
			}
			if j > 0 && v < day[j-1] && !negligible {
				if rel := (day[j-1] - v) / scale; rel > worst {
					worst = rel
				}
			}
		}
		if worst > absurdCrossing {
			return worst, fmt.Errorf("day %d: quantiles are badly out of order "+
				"(largest crossing %.1f%% of the forecast's own size, far beyond the "+
				"~0.2%% a model normally produces)", i, worst*100)
		}
		sort.Float64s(day) // monotonic rearrangement, in place
	}
	return worst, nil
}
