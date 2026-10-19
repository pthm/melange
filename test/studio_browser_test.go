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

	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStudio_Browser drives studio in headless Chrome to check that htmx
// works under the strict CSP: the query form, lazy count badges, the lazy
// group list, infinite scroll, and an inline Explain, with no CSP violation
// or console error along the way. It skips when no Chrome is installed
// (GitHub's ubuntu runners have google-chrome).
func TestStudio_Browser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	// ponytail: no sandbox, since Ubuntu 24.04 runners block the user
	// namespaces Chrome's sandbox needs; the browser only visits the local
	// test server.
	actx, cancel := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.NoSandbox)...)
	defer cancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, time.Minute)
	defer cancel()

	var mu sync.Mutex
	var problems []string
	report := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		problems = append(problems, s)
	}
	reported := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), problems...)
	}
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *cdplog.EventEntryAdded: // CSP violations, failed loads
			if e.Entry.Level == cdplog.LevelError || e.Entry.Source == cdplog.SourceSecurity {
				report(e.Entry.URL + ": " + e.Entry.Text)
			}
		case *runtime.EventExceptionThrown:
			report(e.ExceptionDetails.Error())
		case *runtime.EventConsoleAPICalled:
			if e.Type == runtime.APITypeError || e.Type == runtime.APITypeWarning {
				args := make([]string, 0, len(e.Args))
				for _, a := range e.Args {
					args = append(args, string(a.Value)+a.Description)
				}
				report("console." + string(e.Type) + ": " + strings.Join(args, " "))
			}
		}
	})
	if err := chromedp.Run(ctx, cdplog.Enable()); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			t.Skipf("no Chrome to drive: %v", err)
		}
		require.NoError(t, err)
	}
	f := seedStudio(t, "public")
	c := newStudio(t, openStudioDB(t, f.dsn), "public")

	// Model page: run an Explain through the query form.
	require.NoError(t, chromedp.Run(ctx,
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
	var badge string
	var rows int
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Navigate(entity),
		chromedp.WaitVisible(`#group-list .access-rows li`),
		chromedp.Poll(`[...document.querySelectorAll('.subnav .badge')].every(b => b.textContent.trim() !== '…')`, nil),
		chromedp.Text(`.subnav a[aria-current] .badge`, &badge),
		chromedp.Poll(`(() => {
			const more = document.querySelector('#group-list .more-rows');
			if (more) { more.scrollIntoView(); return false; }
			return true;
		})()`, nil, chromedp.WithPollingInterval(50*time.Millisecond)),
		chromedp.Evaluate(`document.querySelectorAll('#group-list .access-rows > li').length`, &rows),
		chromedp.Click(`#group-list .access-rows > li:first-child button.chip`),
		chromedp.WaitVisible(`#group-list .access-rows > li:first-child .why-out .verdict`),
	))
	assert.Equal(t, "250", strings.TrimSpace(badge))
	assert.Equal(t, pageUsers, rows, "infinite scroll loaded every row")
	assert.Empty(t, reported(), "CSP violations or console errors")

	// The listener does catch a violation: load an image from another origin.
	require.NoError(t, chromedp.Run(ctx,
		chromedp.Evaluate(`document.body.append(Object.assign(new Image(), {src: 'http://192.0.2.1/x.png'}))`, nil),
	))
	require.Eventually(t, func() bool { return len(reported()) > 0 }, 10*time.Second, 50*time.Millisecond)
	assert.Contains(t, strings.Join(reported(), "\n"), "Content Security Policy")
}
