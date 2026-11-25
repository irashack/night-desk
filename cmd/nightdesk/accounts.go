package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/irashack/night-desk/internal/config"
	"github.com/irashack/night-desk/internal/store"
)

// accountsCommand manages local accounts from the command line: what an
// administrator does on the Readers page, for a headless install or a lost
// administrator password.
func accountsCommand(ctx context.Context, c *config.Config, db *store.Store, args []string, out, errOut io.Writer) int {
	if c.AuthMode() != config.AuthLocal {
		fmt.Fprintf(errOut, "server.auth.mode is %s; accounts exist only in local mode\n", c.AuthMode())
		return 1
	}
	now := time.Now()
	link := func(username string) int {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return 1
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		expires := now.Add(c.InviteLifetime())
		if err := db.CreateInvite(ctx, store.Invite{TokenHash: sha256.Sum256([]byte(token)), Username: username, CreatedBy: "command line", Created: now, Expires: expires}); err != nil {
			fmt.Fprintln(errOut, "link creation failed")
			return 1
		}
		fmt.Fprintln(out, strings.TrimRight(c.Server.PublicBaseURL, "/")+"/invite/"+token)
		fmt.Fprintf(errOut, "The link works once, until %s.\n", expires.Format(time.RFC1123))
		return 0
	}
	switch args[0] {
	case "list":
		list, err := db.Accounts(ctx)
		if err != nil {
			fmt.Fprintln(errOut, "account list failed")
			return 1
		}
		if len(list) == 0 {
			fmt.Fprintln(errOut, "No accounts yet: serve prints a setup code, or run 'nightdesk accounts invite' and open the link.")
		}
		for _, a := range list {
			role := "reader"
			if a.Admin {
				role = "administrator"
			}
			fmt.Fprintf(out, "%s\t%s\tsince %s\t%d passkeys\n", a.Username, role, a.Created.Format("2006-01-02"), a.Passkeys)
		}
		return 0
	case "invite":
		// The first account an invite creates on an empty install
		// administers the others, like one made with the setup code.
		return link("")
	case "reset":
		username, _ := store.NormalUsername(args[1])
		if _, err := db.Account(ctx, username); errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintln(errOut, "no such account")
			return 1
		} else if err != nil {
			return 1
		}
		return link(username)
	case "remove":
		username, _ := store.NormalUsername(args[1])
		if err := db.RemoveAccount(ctx, username); errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintln(errOut, "no such account")
			return 1
		} else if err != nil {
			fmt.Fprintln(errOut, err)
			return 1
		}
		fmt.Fprintln(errOut, "Removed "+username+".")
		return 0
	}
	return 2
}
