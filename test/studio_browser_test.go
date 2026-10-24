package test

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStudio_Browser drives studio in headless Chrome to check that htmx
// works under the strict CSP: the query form, lazy count badges, the lazy
// group list, infinite scroll, and an inline Explain, with no CSP violation
// or console error along the way. It skips when no Chrome is installed
// (GitHub's ubuntu runners have google-chrome).
// startBrowser launches headless Chrome for a test and collects CSP
// violations, failed loads, exceptions and console errors or warnings. It
// skips the test when no Chrome is installed (GitHub's ubuntu runners have
// google-chrome).
func startBrowser(t *testing.T) (ctx context.Context, reported func() []string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	// ponytail: no sandbox, since Ubuntu 24.04 runners block the user
	// namespaces Chrome's sandbox needs; the browser only visits the local
	// test server.
	actx, cancel := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox, chromedp.WindowSize(1280, 900))...)
	t.Cleanup(cancel)
	ctx, cancel = chromedp.NewContext(actx)
	t.Cleanup(cancel)
	ctx, cancel = context.WithTimeout(ctx, 3*time.Minute) // includes the launch
	t.Cleanup(cancel)
	if err := chromedp.Do(ctx); err != nil { // launches Chrome
		if errors.Is(err, exec.ErrNotFound) {
			t.Skipf("no Chrome to drive: %v", err)
		}
		require.NoError(t, err)
	}

	var mu sync.Mutex
	var problems []string
	reported = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), problems...)
	}
	messages := chromedp.Console(ctx) // ends with ctx
	go func() {
		for m, err := range messages {
			if err != nil {
				return
			}
			// Exceptions, console.error/warn, and browser-log errors or
			// security entries (CSP violations, failed loads).
			bad := m.IsException() || m.Type == chromedp.ConsoleError || m.Source == "security" ||
				(m.Type == chromedp.ConsoleWarning && m.Source == "")
			if bad {
				mu.Lock()
				problems = append(problems, m.URL+": "+m.String())
				mu.Unlock()
			}
		}
	}()
	return ctx, reported
}

func TestStudio_Browser(t *testing.T) {
	ctx, reported := startBrowser(t)
	f := seedStudio(t, "public")
	c := newStudio(t, openStudioDB(t, f.dsn), "public")

	// Model page: run an Explain through the query form.
	require.NoError(t, chromedp.Do(ctx,
		chromedp.Navigate(c.base+"/"),
		chromedp.WaitVisible(`form.query`),
		chromedp.SetValue(`form.query input[name=subject]`, "user:"+f.users[10]),
		chromedp.SetValue(`form.query input[name=relation]`, "can_read"),
		chromedp.SetValue(`form.query input[name=object]`, "organization:"+f.main),
		chromedp.Click(`form.query button[type=submit]`),
		chromedp.WaitVisible(`#query-result .verdict .badge.ok`),
	))

	// Entity page: the badges and the group list load lazily, scrolling
	// appends the rest of the list, and a relation chip explains itself.
	entity := c.base + "/entity?" + url.Values{"type": {"organization"}, "id": {f.main}, "tab": {"by"}, "group": {"user"}}.Encode()
	require.NoError(t, chromedp.Do(ctx,
		chromedp.Navigate(entity),
		chromedp.WaitVisible(`#group-list .access-rows li`),
		poll(`[...document.querySelectorAll('.subnav .badge')].every(b => b.textContent.trim() !== '…')`),
	))
	badge, err := chromedp.Run(ctx, chromedp.Text(`.subnav a[aria-current] .badge`))
	require.NoError(t, err)
	require.NoError(t, chromedp.Do(ctx,
		poll(`(() => {
			const more = document.querySelector('#group-list .more-rows');
			if (more) { more.scrollIntoView(); return false; }
			return true;
		})()`, chromedp.WithPollingInterval(50*time.Millisecond)),
	))
	rows, err := chromedp.Run(ctx, chromedp.Evaluate[int](`document.querySelectorAll('#group-list .access-rows > li').length`))
	require.NoError(t, err)
	require.NoError(t, chromedp.Do(ctx,
		chromedp.Click(`#group-list .access-rows > li:first-child button.chip`),
		chromedp.WaitVisible(`#group-list .access-rows > li:first-child .why-out .verdict`),
	))
	assert.Equal(t, "250", strings.TrimSpace(badge))
	assert.Equal(t, pageUsers, rows, "infinite scroll loaded every row")
	assert.Empty(t, reported(), "CSP violations or console errors")

	// The listener does catch a violation: load an image from another origin.
	require.NoError(t, chromedp.Do(ctx,
		js(`document.body.append(Object.assign(new Image(), {src: 'http://192.0.2.1/x.png'}))`),
	))
	require.Eventually(t, func() bool { return len(reported()) > 0 }, 10*time.Second, 50*time.Millisecond)
	assert.Contains(t, strings.Join(reported(), "\n"), "Content Security Policy")
}

// TestStudio_BrowserBehaviour checks studio.js and the query form's
// per-mode CSS in a real browser: keyboard shortcuts, the sidebar filter,
// the theme toggle surviving a reload, field visibility per mode, the
// what-if panel, Explain chip toggling, and the Direct tab's filter swap.
func TestStudio_BrowserBehaviour(t *testing.T) {
	ctx, reported := startBrowser(t)
	f := seedStudio(t, "public")
	c := newStudio(t, openStudioDB(t, f.dsn), "public")
	run := func(t *testing.T, steps ...chromedp.Action[chromedp.Void]) {
		t.Helper()
		step(t, ctx, chromedp.Steps(steps...))
	}
	eval := func(t *testing.T, expr string) any {
		t.Helper()
		return step(t, ctx, chromedp.Evaluate[any](expr))
	}
	// clear empties an input the way a user would, so its input listeners run.
	clearInput := func(sel string) chromedp.Action[chromedp.Void] {
		return js(`(() => { const i = document.querySelector('` + sel + `'); i.value = ''; i.dispatchEvent(new Event('input', {bubbles: true})); })()`)
	}

	t.Run("slash focuses the filter, but not while typing", func(t *testing.T) {
		run(t, chromedp.Navigate(c.base+"/"), chromedp.WaitVisible(`form.query`),
			chromedp.KeyEvent("/"))
		assert.Equal(t, true, eval(t, `document.activeElement.matches('.sidebar .filter-bar input')`), "Model page: / focuses the sidebar filter")
		run(t, chromedp.Focus(`form.query input[name=subject]`), chromedp.KeyEvent("/"))
		assert.Equal(t, "/", eval(t, `document.querySelector('form.query input[name=subject]').value`), "typing / in a field types it")
		assert.Equal(t, true, eval(t, `document.activeElement.name === 'subject'`))

		run(t, chromedp.Navigate(c.base+"/browse/user"), chromedp.WaitVisible(`#entities .row`),
			js(`document.activeElement.blur()`), chromedp.KeyEvent("/"))
		assert.Equal(t, true, eval(t, `document.activeElement.matches('.content .filter-bar input')`), "browse: / prefers the main pane's filter")
	})

	t.Run("sidebar filter narrows and reports no matches", func(t *testing.T) {
		run(t, chromedp.Navigate(c.base+"/"), chromedp.WaitVisible(`.sidebar .filter-bar input`),
			chromedp.SendKeys(`.sidebar .filter-bar input`, "can_merge"))
		visible := eval(t, `[...document.querySelectorAll('#model-nav [data-item]')].filter(e => !e.hidden).map(e => e.dataset.text)`)
		assert.Equal(t, []any{"pull_request#can_merge"}, visible)
		assert.Equal(t, []any{"pull_request"}, eval(t, `[...document.querySelectorAll('#model-nav [data-group]')].filter(g => !g.hidden).map(g => g.querySelector('summary').firstChild.textContent.trim())`))
		assert.Equal(t, true, eval(t, `document.querySelector('#model-nav [data-group]:not([hidden])').open`), "a matching type opens")
		run(t, clearInput(`.sidebar .filter-bar input`), chromedp.SendKeys(`.sidebar .filter-bar input`, "zzz"))
		assert.Equal(t, false, eval(t, `document.querySelector('[data-empty-filter]').hidden`), "the empty state shows")
	})

	t.Run("theme toggle persists across a reload", func(t *testing.T) {
		run(t, chromedp.Navigate(c.base+"/health"), chromedp.WaitVisible(`[data-theme-toggle]`),
			js(`localStorage.removeItem('studio-theme'); document.documentElement.dataset.theme`))
		before := eval(t, `document.documentElement.dataset.theme`)
		run(t, chromedp.Click(`[data-theme-toggle]`))
		after := eval(t, `document.documentElement.dataset.theme`)
		assert.NotEqual(t, before, after)
		assert.Equal(t, after, eval(t, `localStorage.getItem('studio-theme')`))
		run(t, chromedp.Reload(), chromedp.WaitVisible(`[data-theme-toggle]`))
		assert.Equal(t, after, eval(t, `document.documentElement.dataset.theme`), "theme.js applies the saved choice on load")
		run(t, js(`localStorage.removeItem('studio-theme')`))
	})

	t.Run("modes show only their fields and what-if only in Explain", func(t *testing.T) {
		run(t, chromedp.Navigate(c.base+"/"), chromedp.WaitVisible(`form.query`))
		shown := func() any {
			return eval(t, `['subject', 'relation', 'object'].filter(n => document.querySelector('form.query input[name=' + n + ']').offsetParent !== null)
				.concat(document.querySelector('details.whatif').offsetParent !== null ? ['whatif'] : [])
				.concat([[...document.querySelectorAll('form.query button[type=submit] span')].find(s => s.offsetParent !== null).textContent])`)
		}
		for mode, want := range map[string][]any{
			"explain": {"subject", "relation", "object", "whatif", "Explain"},
			"expand":  {"relation", "object", "Expand"},
			"matrix":  {"subject", "object", "Matrix"},
			"analyze": {"subject", "relation", "object", "Analyze"},
		} {
			run(t, chromedp.Click(`.modes label:has(input[value=`+mode+`])`))
			assert.Equal(t, want, shown(), mode)
		}
	})

	t.Run("what-if: closed is not sent, Cmd/Ctrl+Enter runs it", func(t *testing.T) {
		outsider := f.users[10] // a member of main only, not of extra[0]
		run(t, chromedp.Navigate(c.base+"/"), chromedp.WaitVisible(`form.query`),
			chromedp.Click(`.modes label:has(input[value=explain])`), // the browser may restore the last mode
			chromedp.SetValue(`form.query input[name=subject]`, "user:"+outsider),
			chromedp.SetValue(`form.query input[name=relation]`, "can_read"),
			chromedp.SetValue(`form.query input[name=object]`, "organization:"+f.extra[0]))
		assert.Equal(t, true, eval(t, `document.querySelector('details.whatif textarea').disabled`), "closed panel: textarea disabled, so not sent")
		// <details> fires toggle asynchronously; studio.js enables the box then.
		run(t, chromedp.Click(`details.whatif summary`), poll(`!document.querySelector('details.whatif textarea').disabled`))
		run(t, chromedp.SendKeys(`details.whatif textarea`, "user:"+outsider+" member organization:"+f.extra[0]),
			js(`document.querySelector('details.whatif textarea').dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', ctrlKey: true, bubbles: true}))`),
			chromedp.WaitVisible(`#query-result .whatif-tuples`))
		assert.Contains(t, step(t, ctx, chromedp.Text(`#query-result .verdict`)), "the what-if tuples grant it")
	})

	t.Run("a second click on an Explain chip collapses it", func(t *testing.T) {
		entity := c.base + "/entity?" + url.Values{"type": {"organization"}, "id": {f.main}, "tab": {"by"}, "group": {"user"}}.Encode()
		chip := `#group-list .access-rows > li:first-child button.chip`
		// Click through the element, as htmx sees it: a coordinate click can
		// land on the sticky page header once the panel shifts the layout.
		click := js(`document.querySelector('` + chip + `').click()`)
		run(t, chromedp.Navigate(entity), chromedp.WaitVisible(chip),
			poll(`!!document.querySelector('`+chip+`')['htmx-internal-data']`), // htmx has processed it
			click, chromedp.WaitVisible(`#group-list .access-rows > li:first-child .why-out .verdict`))
		assert.Equal(t, "true", eval(t, `document.querySelector('`+chip+`').getAttribute('aria-expanded')`))
		run(t, click, poll(`document.querySelector('#group-list .access-rows > li:first-child .why-out').childElementCount === 0`))
		assert.Equal(t, "false", eval(t, `document.querySelector('`+chip+`').getAttribute('aria-expanded')`))
	})

	t.Run("direct tab filter swaps tables and keeps the input", func(t *testing.T) {
		direct := c.base + "/entity?" + url.Values{"type": {"organization"}, "id": {f.main}, "tab": {"direct"}}.Encode()
		run(t, chromedp.Navigate(direct), chromedp.WaitVisible(`#direct-tables tbody tr`),
			chromedp.SendKeys(`#direct .filter-bar input`, "owner"),
			poll(`[...document.querySelectorAll('#direct-tables .sec-head .badge')].some(b => b.textContent.includes(' of '))`))
		assert.Equal(t, "owner", eval(t, `document.querySelector('#direct .filter-bar input').value`))
		assert.Equal(t, true, eval(t, `[...document.querySelectorAll('#direct-tables tbody tr:not(.more-rows)')].every(r => r.textContent.includes('owner'))`))
	})

	assert.Empty(t, reported(), "CSP violations or console errors")
}

// js runs a JavaScript expression and ignores its value.
func js(expr string) chromedp.Action[chromedp.Void] { return chromedp.Evaluate[chromedp.Void](expr) }

// poll waits until a JavaScript expression is truthy.
func poll(expr string, opts ...chromedp.PollOption) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, t *chromedp.Target) error {
		_, err := chromedp.Poll[any](expr, opts...)(ctx, t)
		return err
	})
}

// step runs one action under its own deadline, so a hang fails that subtest
// alone.
func step[T any](t *testing.T, ctx context.Context, a chromedp.Action[T]) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := chromedp.Run(ctx, a)
	require.NoError(t, err)
	return out
}
