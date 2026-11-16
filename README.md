# eclass-mcp

An MCP server for [Open eClass](https://www.openeclass.org/), the learning platform most Greek universities run. Plug it into Claude Code, Claude Desktop, Cursor, VS Code, Codex, Gemini CLI or any other MCP client and your assistant can read your courses, grab your documents, check deadlines, post in forums and submit assignments for you.

It was built for [eclass.uniwa.gr](https://eclass.uniwa.gr) (University of West Attica, eClass 4.4) and should work with any Open eClass 4.x site that has the mobile API turned on. Most do, because that is what the official eClass app uses.

## What you can ask it

- "What's new in my courses this week?"
- "Which assignments are due before Friday, and have I submitted them?"
- "Download all the lecture slides from Operating Systems."
- "Summarise the last announcements from my lab course."
- "Upload ~/thesis/report.pdf to assignment 3 of Databases."
- "Reply to the latest forum thread in Networks saying I'll be at the lab."
- "Show me my grades."

## Install

You need Go 1.26 or newer.

```sh
go install github.com/marios-pz/eclass-mcp@latest
```

That drops an `eclass-mcp` binary in `$(go env GOPATH)/bin` (usually `~/go/bin`). Make sure it is on your `PATH`, or use the full path in the configs below.

Building from a clone works too:

```sh
git clone https://github.com/marios-pz/eclass-mcp
cd eclass-mcp
go build -o eclass-mcp .
```

## Configuration

Everything is set with environment variables. Your MCP client passes them to the server when it starts it.

| Variable | Required | Default | What it is |
|---|---|---|---|
| `ECLASS_USERNAME` | yes | | Your eClass username |
| `ECLASS_PASSWORD` | yes | | Your eClass password |
| `ECLASS_URL` | no | `https://eclass.uniwa.gr` | Your university's eClass address |
| `ECLASS_DOWNLOAD_DIR` | no | `~/Downloads/eclass` | Where downloaded files are saved |

Your credentials never leave your machine except to log in to the eClass site itself. The server refuses to send your session to any other host.

## Hooking it up

### Claude Code

```sh
claude mcp add eclass --scope user \
  -e ECLASS_USERNAME=your_username \
  -e ECLASS_PASSWORD='your_password' \
  -- eclass-mcp
```

`--scope user` makes it available in every project. Run `claude mcp list` to check it connected, or `/mcp` inside a session.

### Claude Desktop

Open Settings, then Developer, then Edit Config, and add:

```json
{
  "mcpServers": {
    "eclass": {
      "command": "eclass-mcp",
      "env": {
        "ECLASS_USERNAME": "your_username",
        "ECLASS_PASSWORD": "your_password"
      }
    }
  }
}
```

Claude Desktop does not always see your shell `PATH`, so if it can't find the binary use the full path, for example `/home/you/go/bin/eclass-mcp`.

### Cursor, Windsurf, Gemini CLI

These use the same `mcpServers` block as Claude Desktop above. Put it in:

- Cursor: `~/.cursor/mcp.json`
- Windsurf: `~/.codeium/windsurf/mcp_config.json`
- Gemini CLI: `~/.gemini/settings.json`

### VS Code (Copilot agent mode)

Create `.vscode/mcp.json` in a workspace, or run "MCP: Open User Configuration" for a global one. VS Code can prompt for the password so it never sits in a file:

```json
{
  "inputs": [
    { "type": "promptString", "id": "eclass-password", "description": "eClass password", "password": true }
  ],
  "servers": {
    "eclass": {
      "type": "stdio",
      "command": "eclass-mcp",
      "env": {
        "ECLASS_USERNAME": "your_username",
        "ECLASS_PASSWORD": "${input:eclass-password}"
      }
    }
  }
}
```

### Codex CLI

In `~/.codex/config.toml`:

```toml
[mcp_servers.eclass]
command = "eclass-mcp"
env = { ECLASS_USERNAME = "your_username", ECLASS_PASSWORD = "your_password" }
```

### Anything else

It is a plain stdio MCP server. Run the `eclass-mcp` command with the two environment variables set and you're done.

## The tools

There are five, on purpose. eClass has around forty modules and every university turns on a different mix, so instead of one tool per screen the server gives the assistant a way to browse eClass the way you would, plus a map of where everything is.

| Tool | What it does |
|---|---|
| `eclass_my_courses` | Your name, every course you're enrolled in (with its course code) and your personal tools: messages, calendar, announcements from all courses, notes, profile, usage stats. |
| `eclass_course_tools` | Every module enabled in one course, with the link to open it. The list comes from your eClass, so it always matches what you see in the browser. |
| `eclass_get_page` | Opens any eClass page and turns it into clean markdown. Links come out absolute, every form is listed with all its fields and current values, and lists that load through JavaScript get their data URLs listed so the assistant can fetch the JSON. Long pages come in chunks. |
| `eclass_submit_form` | Fills in and sends a form, including file uploads. That's how assignments get submitted, forum replies posted, messages sent, quizzes answered and so on. |
| `eclass_download` | Saves a file (a PDF from Documents, an assignment attachment, a zipped folder) to your download folder. |

The server also hands the assistant a short guide on connect, so it knows to start from your course list, follow links, and send hidden form fields like eClass's CSRF `token`.

## What's covered

Because the assistant can open any page and submit any form your account can, it can do anything you can do in the browser. Here is how that maps onto eClass:

| eClass area | What the assistant can do |
|---|---|
| Course list, course home, course description, units | Read everything, follow every link |
| Announcements (per course and "my announcements") | Read, search, and post if you're the teacher |
| Documents | Browse folders, download files and whole folders |
| Assignments | Read the brief and deadline, see your submission and grade, upload a submission |
| Exercises | Read the questions, answer and submit (it will ask you before a final submission) |
| Forum | Read threads, reply, start new topics |
| Messages | Read your inbox and sent items, write and reply to messages |
| Agenda, personal calendar | Read events and deadlines, add personal events |
| Gradebook, attendance, progress, certificates and badges | Read |
| Polls and questionnaires | Read and answer |
| Links, videos, glossary, e-book, wiki, blog, wall, comments | Read, and write where eClass lets you |
| Learning paths | Read the structure and open the material |
| Groups | See your groups, join or leave where allowed, use group forums and documents |
| Course registration and search | Search the catalogue, enroll and unregister |
| Profile, notes, usage stats | Read and edit |
| Teacher and admin pages | Everything your role allows, through the same pages and forms |

A few things live entirely inside the browser and can't really be driven by an assistant: interactive H5P activities, the live chat window, editing mind maps, drag and drop widgets and joining video calls. Their content and links can still be read.

## Safety

The assistant acts as you. Anything you could click on eClass, it can click too, and some plain links (delete, unregister, logout) trigger actions on their own. The server tells the assistant to ask you before anything irreversible, and `eclass_submit_form` is marked as destructive so clients that respect that hint will ask for confirmation. If you only want to read, don't approve `eclass_submit_form` calls, or deny the tool in your client's settings.

Keep your password out of shared config files and repos. VS Code's input prompt or your client's secret storage is the nicest option where it exists.

## How it works

Login goes through `modules/mobile/mlogin.php`, the same endpoint the official eClass app uses. That returns a PHP session id, which the server then uses as a normal cookie for every web page. eClass marks the session as "mobile", which conveniently strips most of the site's chrome from the HTML. When the session expires the server notices (the mobile API answers `EXPIRED`, web pages bounce to the login form), logs in again and retries.

Pages are converted with [html-to-markdown](https://github.com/JohannesKaufmann/html-to-markdown) and [goquery](https://github.com/PuerkitoBio/goquery), and the MCP side uses the official [Go SDK](https://github.com/modelcontextprotocol/go-sdk).

The code is small:

- `main.go` reads the config and starts the stdio server
- `client.go` does login, session renewal, requests and uploads
- `page.go` turns HTML into markdown and lists forms and data endpoints
- `tools.go` defines the five tools
- `eclass_test.go` covers page rendering and the login retry logic

## Troubleshooting

**"wrong username or password"**: double check by logging in on the website. eClass also blocks an IP for a while after several failed attempts, so wait a bit before trying again.

**"the mobile API is disabled on this eClass instance"**: your university turned it off. Ask the eClass admins to enable it in the platform configuration.

**The client says it can't find `eclass-mcp`**: use the absolute path to the binary in your config.

**Your university uses single sign-on (CAS, Shibboleth)**: the mobile login needs a username and password that eClass itself can check (local accounts, and usually LDAP ones too). Accounts that can only log in through a browser redirect aren't supported yet.

## Development

```sh
go test ./...
go vet ./...
```

To poke at it by hand, the [MCP Inspector](https://github.com/modelcontextprotocol/inspector) is handy:

```sh
npx @modelcontextprotocol/inspector -e ECLASS_USERNAME=you -e ECLASS_PASSWORD=secret eclass-mcp
```
