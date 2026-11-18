// Package grants — read.go
//
// Reading accounts from grant tables: the Source's (background pool) or
// mysqld's (control channel). Only granted columns of mysql.user are read;
// the password hash column never is (product doc 11 §6).
package grants

import (
	"context"
	"encoding/json"
	"strings"
)

// Querier runs a read and returns rows of strings ("" for NULL).
type Querier func(ctx context.Context, sql string) ([][]string, error)

// privColumns maps mysql.user and mysql.db columns to privileges.
var privColumns = [][2]string{
	{"Select_priv", "SELECT"}, {"Insert_priv", "INSERT"}, {"Update_priv", "UPDATE"}, {"Delete_priv", "DELETE"},
	{"Create_priv", "CREATE"}, {"Drop_priv", "DROP"}, {"Alter_priv", "ALTER"}, {"Index_priv", "INDEX"},
	{"References_priv", "REFERENCES"}, {"Create_view_priv", "CREATE VIEW"}, {"Show_view_priv", "SHOW VIEW"},
	{"Create_routine_priv", "CREATE ROUTINE"}, {"Alter_routine_priv", "ALTER ROUTINE"}, {"Execute_priv", "EXECUTE"},
	{"Trigger_priv", "TRIGGER"}, {"Create_tmp_table_priv", "CREATE TEMPORARY TABLES"}, {"Lock_tables_priv", "LOCK TABLES"},
}

func cols() string {
	var c []string
	for _, p := range privColumns {
		c = append(c, p[0])
	}
	return strings.Join(c, ", ")
}

// Internal accounts mysqld needs and VDB never mirrors or drops.
func Internal(p Principal) bool {
	return strings.HasPrefix(p.User, "mysql.") || (p.User == "vdb_admin" && p.Host == "localhost")
}

// ReadAccounts reads every account with its direct privileges, roles and
// default roles. skip filters accounts out (internal ones, the service account).
func ReadAccounts(ctx context.Context, q Querier, skip func(Principal) bool) (map[Principal]*Account, error) {
	rows, err := q(ctx, "SELECT User, Host, plugin, ssl_type, account_locked, password_expired, IFNULL(User_attributes,''), "+cols()+" FROM mysql.user")
	if err != nil {
		return nil, err
	}
	out := map[Principal]*Account{}
	for _, r := range rows {
		p := Principal{User: r[0], Host: r[1]}
		if skip != nil && skip(p) {
			continue
		}
		a := &Account{Principal: p, Method: r[2], RequireSSL: r[3] != "", Locked: r[4] == "Y", Atoms: map[Atom]bool{}}
		for i, pc := range privColumns {
			if r[7+i] == "Y" {
				a.Atoms[Atom{Priv: pc[1], DB: "*", Table: "*"}] = true
			}
		}
		a.Revokes = partialRevokes(r[6])
		out[p] = a
	}
	get := func(u, h string) *Account { return out[Principal{u, h}] }
	if rows, err = q(ctx, "SELECT User, Host, Db, "+cols()+" FROM mysql.db"); err == nil {
		for _, r := range rows {
			a := get(r[0], r[1])
			if a == nil {
				continue
			}
			for i, pc := range privColumns {
				if r[3+i] == "Y" {
					a.Atoms[Atom{Priv: pc[1], DB: r[2], Table: "*"}] = true
				}
			}
		}
	} else {
		return nil, err
	}
	if rows, err = q(ctx, "SELECT User, Host, Db, Table_name, Table_priv FROM mysql.tables_priv"); err == nil {
		for _, r := range rows {
			if a := get(r[0], r[1]); a != nil {
				for _, x := range strings.Split(r[4], ",") {
					if x = strings.ToUpper(strings.TrimSpace(x)); x != "" {
						a.Atoms[Atom{Priv: x, DB: r[2], Table: r[3]}] = true
					}
				}
			}
		}
	}
	if rows, err = q(ctx, "SELECT User, Host, Db, Table_name, Column_name, Column_priv FROM mysql.columns_priv"); err == nil {
		for _, r := range rows {
			if a := get(r[0], r[1]); a != nil {
				for _, x := range strings.Split(r[5], ",") {
					if x = strings.ToUpper(strings.TrimSpace(x)); x != "" {
						a.Atoms[Atom{Priv: x, DB: r[2], Table: r[3], Col: r[4]}] = true
					}
				}
			}
		}
	}
	if rows, err = q(ctx, "SELECT User, Host, Db, Routine_name, Routine_type, Proc_priv FROM mysql.procs_priv"); err == nil {
		for _, r := range rows {
			if a := get(r[0], r[1]); a != nil {
				for _, x := range strings.Split(r[5], ",") {
					if x = strings.ToUpper(strings.TrimSpace(x)); x != "" {
						a.Atoms[Atom{Priv: x, DB: r[2], Table: r[3], Routine: strings.ToUpper(r[4])}] = true
					}
				}
			}
		}
	}
	if rows, err = q(ctx, "SELECT FROM_USER, FROM_HOST, TO_USER, TO_HOST FROM mysql.role_edges"); err == nil {
		for _, r := range rows {
			if a := get(r[2], r[3]); a != nil {
				a.Roles = append(a.Roles, Principal{r[0], r[1]})
			}
			if role := get(r[0], r[1]); role != nil {
				role.Role = true
			}
		}
	}
	if rows, err = q(ctx, "SELECT USER, HOST, DEFAULT_ROLE_USER, DEFAULT_ROLE_HOST FROM mysql.default_roles"); err == nil {
		for _, r := range rows {
			if a := get(r[0], r[1]); a != nil {
				a.DefaultRoles = append(a.DefaultRoles, Principal{r[2], r[3]})
			}
		}
	}
	return out, nil
}

// partialRevokes reads {"Restrictions": [{"Database": "x", "Privileges": [...]}]}.
func partialRevokes(ua string) []Atom {
	if ua == "" {
		return nil
	}
	var v struct {
		Restrictions []struct {
			Database   string
			Privileges []string
		}
	}
	if json.Unmarshal([]byte(ua), &v) != nil {
		return nil
	}
	var out []Atom
	for _, r := range v.Restrictions {
		for _, p := range r.Privileges {
			out = append(out, Atom{Priv: strings.ToUpper(p), DB: r.Database, Table: "*"})
		}
	}
	return out
}
