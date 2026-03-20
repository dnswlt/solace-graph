package report

import (
	"html/template"
	"io"
	"time"

	"github.com/dnswlt/solace-graph/internal/graph"
)

type reportData struct {
	Nodes       []graph.Node
	GeneratedAt time.Time
}

const htmlTemplate = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Solace Dependency Graph</title>
    <style>
        :root {
            --primary-color: #2563eb;
            --text-main: #1e293b;
            --text-muted: #64748b;
            --bg-body: #f8fafc;
            --bg-card: #ffffff;
            --border-color: #e2e8f0;
            --code-bg: #f1f5f9;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            margin: 0;
            padding: 0;
            background-color: var(--bg-body);
            color: var(--text-main);
            line-height: 1.4;
        }

        .container {
            max-width: 1600px;
            margin: 0 auto;
            padding: 1.5rem;
        }

        header {
            margin-bottom: 1.5rem;
            border-bottom: 1px solid var(--border-color);
            padding-bottom: 0.5rem;
        }

        h1 { margin: 0; font-size: 1.5rem; font-weight: 700; color: var(--text-main); }
        h2 { font-size: 1.25rem; margin-top: 1.5rem; margin-bottom: 0.75rem; }
        h3 { font-size: 0.9rem; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 0.75rem; }

        .filter-box {
            margin-bottom: 1rem;
        }

        #app-filter {
            width: 100%;
            padding: 0.5rem;
            border: 1px solid var(--border-color);
            border-radius: 4px;
            font-size: 0.9rem;
            box-sizing: border-box;
            outline: none;
            transition: border-color 0.2s;
            color: var(--text-main);
        }

        #app-filter:focus {
            border-color: var(--primary-color);
        }

        nav {
            background: var(--bg-card);
            padding: 1rem;
            border-radius: 6px;
            border: 1px solid var(--border-color);
            margin-bottom: 2rem;
        }

        nav ul {
            list-style: none;
            padding: 0;
            margin: 0;
            display: flex;
            flex-direction: column;
            gap: 0.25rem;
            max-height: 400px;
            overflow-y: auto;
        }

        nav a {
            text-decoration: none;
            color: var(--primary-color);
            font-size: 0.9rem;
            padding: 0.125rem 0.25rem;
            border-radius: 4px;
            display: block;
        }

        nav a:hover { background: var(--code-bg); }

        .app-card {
            background: var(--bg-card);
            border: 1px solid var(--border-color);
            border-radius: 8px;
            padding: 1.25rem;
            margin-bottom: 2rem;
            box-shadow: 0 1px 2px rgba(0, 0, 0, 0.05);
        }

        .app-header {
            display: flex;
            align-items: center;
            justify-content: space-between;
            margin-bottom: 0.75rem;
            border-bottom: 1px solid var(--border-color);
            padding-bottom: 0.5rem;
        }

        .app-header h2 { margin: 0; border: none; }
        .app-meta { font-size: 0.8rem; color: var(--text-muted); }
        .discovery-tag {
            background: var(--code-bg);
            padding: 0.125rem 0.5rem;
            border-radius: 4px;
            font-family: ui-monospace, SFMono-Regular, monospace;
            font-size: 0.7rem;
            color: var(--text-main);
        }

        .files-list { margin-top: 0.5rem; }
        .file-path {
            display: block;
            font-family: ui-monospace, monospace;
            font-size: 0.75rem;
            color: var(--text-muted);
            padding: 1px 0;
        }

        .rel-table {
            width: 100%;
            border-collapse: collapse;
            margin-top: 0.5rem;
            table-layout: auto;
        }

        .rel-table th {
            text-align: left;
            font-size: 0.75rem;
            color: var(--text-muted);
            padding: 0.5rem;
            border-bottom: 1px solid var(--border-color);
        }

        .rel-table th:first-child,
        .rel-row td:first-child {
            width: 1%;
            white-space: nowrap;
            padding-right: 2rem;
        }

        .rel-row td {
            padding: 0.75rem 0.5rem;
            vertical-align: top;
            border-bottom: 1px solid var(--border-color);
        }

        .rel-app-name { font-weight: 600; color: var(--primary-color); text-decoration: none; font-size: 0.95rem; }
        .rel-app-name:hover { text-decoration: underline; }

        .rel-summary {
            font-size: 0.85rem;
            font-weight: 600;
            color: var(--text-main);
            margin-bottom: 0.5rem;
            display: flex;
            align-items: center;
            gap: 0.5rem;
        }

        .match-details summary {
            font-size: 0.75rem;
            color: var(--primary-color);
            cursor: pointer;
            user-select: none;
        }

        .match-container { display: flex; flex-direction: column; gap: 0.5rem; margin-top: 0.5rem; }
        .match-flow {
            background: #fafafa;
            border: 1px solid var(--border-color);
            border-radius: 4px;
            padding: 0.75rem;
            display: flex;
            align-items: stretch;
            gap: 1rem;
        }

        .flow-arrow {
            color: var(--text-muted);
            font-size: 1.25rem;
            opacity: 0.3;
            flex: 0 0 20px;
            display: flex;
            flex-direction: column;
            align-items: center;
            justify-content: center;
        }

        .match-endpoints {
            flex: 1;
            display: flex;
            flex-direction: column;
            gap: 0.25rem;
        }

        .endpoint { padding: 4px 8px; border-radius: 4px; }
        .endpoint.is-local { background: #f1f5f9; border: 1px solid var(--border-color); }
        
        .endpoint-label {
            font-size: 0.6rem;
            text-transform: uppercase;
            letter-spacing: 0.05em;
            color: var(--text-muted);
            margin-bottom: 2px;
            display: block;
        }
        .endpoint.is-local .endpoint-label { color: var(--primary-color); font-weight: 700; }

        .topic {
            font-family: ui-monospace, monospace;
            font-weight: 600;
            font-size: 0.85rem;
            word-break: break-all;
            display: block;
        }

        .binding-sub { font-size: 0.75rem; color: var(--text-muted); display: block; }

        details.all-bindings {
            margin-top: 0.5rem;
            padding-top: 0.75rem;
        }

        details.all-bindings summary {
            cursor: pointer;
            font-weight: 600;
            font-size: 0.85rem;
            color: var(--text-muted);
        }

        .bindings-table {
            width: 100%;
            border-collapse: collapse;
            margin-top: 0.75rem;
            font-size: 0.8rem;
        }

        .bindings-table th {
            background: var(--code-bg);
            padding: 0.4rem;
            text-align: left;
            border: 1px solid var(--border-color);
        }

        .bindings-table td {
            padding: 0.4rem;
            border: 1px solid var(--border-color);
            word-break: break-all;
        }

        .bindings-table code { font-family: ui-monospace, monospace; }

    </style>
</head>
<body>
    <div class="container">
        <header>
            <h1>Solace Dependency Graph</h1>
        </header>

        <div class="filter-box">
            <input type="text" id="app-filter" placeholder="Filter by application name..." autocomplete="off">
        </div>
        
        <nav>
            <h3>Applications</h3>
            <ul id="nav-list">
            {{range .Nodes}}
                <li data-name="{{.App.Name}}"><a href="#{{.App.Name}}">{{.App.Name}}</a></li>
            {{end}}
            </ul>
        </nav>

        <div id="apps-container">
        {{range .Nodes}}
        <div class="app-card" id="{{.App.Name}}" data-name="{{.App.Name}}">
            <div class="app-header">
                <h2>{{.App.Name}}{{if .App.Version}} <span style="font-weight: normal; color: var(--text-muted); font-size: 1rem;">v{{.App.Version}}</span>{{end}}</h2>
                <div class="app-meta">
                    Discovered via <span class="discovery-tag">{{.App.Discovery}}</span>
                </div>
            </div>

            <div class="files-list">
                {{range .App.Files}}
                <span class="file-path">{{.}}</span>
                {{end}}
            </div>

            <div style="margin-top: 1.5rem;">
                <h3>Solace Relationships</h3>
                {{if .Edges}}
                <table class="rel-table">
                    <tbody>
                    {{range .Edges}}
                        {{$dir := .Direction}}
                        <tr class="rel-row">
                            <td><a href="#{{.To}}" class="rel-app-name">{{.To}}</a></td>
                            <td>
                                <div class="rel-summary">
                                    {{if eq $dir "both"}}
                                        Bidirectional
                                    {{else if eq $dir "from"}}
                                        Remote &rarr; Local
                                    {{else}}
                                        Local &rarr; Remote
                                    {{end}}
                                    <span style="font-weight: normal; color: var(--text-muted);">({{len .Matches}} matches)</span>
                                </div>
                                <details class="match-details">
                                    <summary>Show individual matches</summary>
                                    <div class="match-container">
                                        {{range .Matches}}
                                        <div class="match-flow">
                                            <div class="flow-arrow">
                                                <div style="flex: 1; border-left: 1px solid var(--border-color); margin-bottom: 2px;"></div>
                                                <div>&darr;</div>
                                                <div style="flex: 1; border-left: 1px solid var(--border-color); margin-top: 2px;"></div>
                                            </div>
                                            <div class="match-endpoints">
                                                {{if eq .Direction "from"}}
                                                <div class="endpoint">
                                                    <span class="endpoint-label">Producer (Remote)</span>
                                                    <span class="topic">{{.Remote.Destination}}</span>
                                                    <span class="binding-sub">{{.Remote.BindingName}}</span>
                                                </div>
                                                <div class="endpoint is-local">
                                                    <span class="endpoint-label">Consumer (Local)</span>
                                                    <span class="topic">{{.Local.Destination}}</span>
                                                    <span class="binding-sub">{{.Local.BindingName}}</span>
                                                </div>
                                                {{else}}
                                                <div class="endpoint is-local">
                                                    <span class="endpoint-label">Producer (Local)</span>
                                                    <span class="topic">{{.Local.Destination}}</span>
                                                    <span class="binding-sub">{{.Local.BindingName}}</span>
                                                </div>
                                                <div class="endpoint">
                                                    <span class="endpoint-label">Consumer (Remote)</span>
                                                    <span class="topic">{{.Remote.Destination}}</span>
                                                    <span class="binding-sub">{{.Remote.BindingName}}</span>
                                                </div>
                                                {{end}}
                                            </div>
                                        </div>
                                        {{end}}
                                    </div>
                                </details>
                            </td>
                        </tr>
                    {{end}}
                    </tbody>
                </table>
                {{else}}
                <p style="color: var(--text-muted); font-size: 0.85rem;">No Solace relationships discovered.</p>
                {{end}}
            </div>

            <details class="all-bindings">
                <summary>All Bindings ({{len .App.Bindings}})</summary>
                <table class="bindings-table">
                    <thead>
                        <tr>
                            <th>Binding Name</th>
                            <th>Dir</th>
                            <th>Destination</th>
                            <th>Binder Type</th>
                        </tr>
                    </thead>
                    <tbody>
                    {{range .App.Bindings}}
                        <tr>
                            <td>{{.BindingName}}</td>
                            <td>{{.Direction}}</td>
                            <td><code>{{.Destination}}</code></td>
                            <td>{{.BinderType}}</td>
                        </tr>
                    {{end}}
                    </tbody>
                </table>
            </details>
        </div>
        {{end}}
        </div>
        <footer style="margin-top: 2rem; padding-top: 0.75rem; border-top: 1px solid var(--border-color); font-size: 0.75rem; color: var(--text-muted);">
            Generated on {{.GeneratedAt.Format "2006-01-02 15:04:05 MST"}}
        </footer>
    </div>

    <script>
        const filterInput = document.getElementById('app-filter');
        const navItems = document.querySelectorAll('#nav-list li');
        const appCards = document.querySelectorAll('.app-card');

        function updateFilter(query) {
            query = query.toLowerCase();
            navItems.forEach(item => {
                const name = item.getAttribute('data-name').toLowerCase();
                item.style.display = name.includes(query) ? '' : 'none';
            });

            appCards.forEach(card => {
                const name = card.getAttribute('data-name').toLowerCase();
                card.style.display = name.includes(query) ? '' : 'none';
            });
        }

        filterInput.addEventListener('input', (e) => {
            updateFilter(e.target.value);
        });

        window.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                filterInput.value = '';
                updateFilter('');
                filterInput.blur();
            } else if (e.key === '/' && document.activeElement !== filterInput) {
                e.preventDefault();
                filterInput.focus();
            }
        });
    </script>
</body>
</html>
`

var reportTemplate = template.Must(template.New("report").Parse(htmlTemplate))

// Generate produces an HTML report from the dependency graph nodes.
func Generate(w io.Writer, nodes []graph.Node) error {
	return reportTemplate.Execute(w, reportData{
		Nodes:       nodes,
		GeneratedAt: time.Now(),
	})
}
