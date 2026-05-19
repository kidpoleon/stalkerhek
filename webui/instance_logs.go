package webui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	instanceLogMaxEntries = 8000
	instanceLogRetention  = 48 * time.Hour // 2 days
)

type logEntry struct {
	at   time.Time
	line string
}

type instanceLogHub struct {
	mu         sync.RWMutex
	maxEntries int
	retention  time.Duration
	entries    []logEntry
	subs       map[chan string]struct{}
}

func newInstanceLogHub(maxEntries int, retention time.Duration) *instanceLogHub {
	if maxEntries <= 0 {
		maxEntries = instanceLogMaxEntries
	}
	if retention <= 0 {
		retention = instanceLogRetention
	}
	return &instanceLogHub{
		maxEntries: maxEntries,
		retention:  retention,
		entries:    make([]logEntry, 0, 256),
		subs:       map[chan string]struct{}{},
	}
}

var (
	instMu  sync.Mutex
	instHub = newInstanceLogHub(instanceLogMaxEntries, instanceLogRetention)
)

type instanceLogWriter struct {
	under io.Writer
	buf   string
}

func (w *instanceLogWriter) Write(p []byte) (int, error) {
	// Always write through to underlying writer first.
	if w.under != nil {
		_, _ = w.under.Write(p)
	}

	w.buf += string(p)
	for {
		i := strings.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(w.buf[:i], "\r")
		w.buf = w.buf[i+1:]
		appendInstanceLogLine(line)
	}
	return len(p), nil
}

func appendInstanceLogLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	now := time.Now()
	instHub.mu.Lock()
	cutoff := now.Add(-instHub.retention)
	kept := instHub.entries[:0]
	for _, e := range instHub.entries {
		if e.at.After(cutoff) {
			kept = append(kept, e)
		}
	}
	instHub.entries = kept
	if len(instHub.entries) >= instHub.maxEntries {
		instHub.entries = instHub.entries[len(instHub.entries)-instHub.maxEntries+1:]
	}
	instHub.entries = append(instHub.entries, logEntry{at: now, line: line})
	for ch := range instHub.subs {
		select {
		case ch <- line:
		default:
		}
	}
	instHub.mu.Unlock()
}

// InitInstanceLogCapture replaces the global log output with a tee that
// writes to the provided underlying writer while also buffering for /logs.
// Call this early in main() to capture startup logs.
func InitInstanceLogCapture(under io.Writer) io.Writer {
	instMu.Lock()
	defer instMu.Unlock()
	return &instanceLogWriter{under: under}
}

func RegisterInstanceLogHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(instanceLogsPageHTML()))
	})

	mux.HandleFunc("/api/logs/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		ch := make(chan string, 64)

		instHub.mu.Lock()
		instHub.subs[ch] = struct{}{}
		history := make([]string, len(instHub.entries))
		for i, e := range instHub.entries {
			history[i] = e.line
		}
		instHub.mu.Unlock()

		defer func() {
			instHub.mu.Lock()
			delete(instHub.subs, ch)
			instHub.mu.Unlock()
			close(ch)
		}()

		write := func(line string) error {
			payload, _ := json.Marshal(map[string]any{"ts": time.Now().Format(time.RFC3339), "line": line})
			_, err := fmt.Fprintf(w, "event: log\ndata: %s\n\n", payload)
			if err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}

		for _, line := range history {
			if err := write(line); err != nil {
				return
			}
		}

		ka := time.NewTicker(15 * time.Second)
		defer ka.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ka.C:
				_, _ = fmt.Fprintf(w, ": keepalive\n\n")
				flusher.Flush()
			case line := <-ch:
				if err := write(line); err != nil {
					return
				}
			}
		}
	})
}

func instanceLogsPageHTML() string {
	return `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="https://i.ibb.co/MyxmyVzz/STALKERHEK-LOGO-1500x1500.png">
  <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/font-awesome/6.5.1/css/all.min.css" referrerpolicy="no-referrer" />
  <title>Stalkerhek Logs</title>
  <style>
    :root{--bg:#0a0f0a;--panel:#0d1410;--border:#1f2e23;--text:#e0e6e0;--muted:#9aaa9a;--brand:#2d7a4e;--brand-hover:#3a8f5e}
    *{box-sizing:border-box}
    body{margin:0;font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Helvetica,Arial,sans-serif;background:linear-gradient(180deg, #0d1410 0%, #0a0f0a 100%);color:var(--text);min-height:100dvh}
    a{color:var(--brand);text-decoration:none} a:hover{color:var(--brand-hover);text-decoration:underline}
    .wrap{max-width:1200px;margin:0 auto;
      padding-top:calc(clamp(22px, 4.2vw, 40px) + env(safe-area-inset-top));
      padding-left:calc(clamp(20px, 4vw, 36px) + env(safe-area-inset-left));
      padding-right:calc(clamp(20px, 4vw, 36px) + env(safe-area-inset-right));
      padding-bottom:calc(clamp(18px, 3.2vw, 32px) + env(safe-area-inset-bottom));
      min-height:100dvh;display:flex;flex-direction:column;gap:14px}
    h1{margin:0 0 6px 0;font-size:20px}
    .sub{color:var(--muted);font-size:13px;line-height:1.45;margin-bottom:12px}
    .box{border:1px solid var(--border);border-radius:16px;background:rgba(15,22,18,.7);padding:14px;flex:1 1 auto;min-height:60vh;max-height:74vh;overflow:auto;scrollbar-gutter:stable}
    .ln{font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,"Liberation Mono","Courier New",monospace;font-size:12.5px;line-height:1.45;padding:6px 8px;border-bottom:1px dashed rgba(31,46,35,.65);white-space:pre-wrap}
    .ln:last-child{border-bottom:none}
    .ts{color:var(--muted);margin-right:10px}
  </style>
</head>
<body>
  <div class="wrap">
    <div>
      <h1><i class="fa-regular fa-file-lines"></i> Instance Logs</h1>
      <div class="sub">Live logs (kept up to 2 days, max ~8000 lines). Service recycles every 24h; profiles on disk are kept. <a href="/dashboard"><i class="fa-solid fa-arrow-left"></i> Back to Dashboard</a></div>
    </div>
    <div class="box" id="box"></div>
  </div>
  <script>
    const box=document.getElementById('box');
    function addLine(ts,line){
      const div=document.createElement('div');
      div.className='ln';
      const t=document.createElement('span');
      t.className='ts';
      t.textContent='['+ts+'] ';
      div.appendChild(t);
      div.appendChild(document.createTextNode(line||''));
      box.appendChild(div);
      box.scrollTop=box.scrollHeight;
    }
    const es=new EventSource('/api/logs/stream');
    es.addEventListener('log',(ev)=>{
      try{
        const d=JSON.parse(ev.data);
        addLine(d.ts||new Date().toISOString(), d.line||'');
      }catch(e){
        addLine(new Date().toISOString(), ev.data||'');
      }
    });
    es.onerror=()=>{ addLine(new Date().toISOString(), 'connection lost; retrying...'); };
  </script>
</body>
</html>`
}
