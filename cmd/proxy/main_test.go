package main

import "testing"

func TestLoadAccountsFromJSON(t *testing.T) {
	t.Setenv("UPSTREAM_ACCOUNTS_JSON", `[{"id":"acct-a","api_key":"sk-a","base_url":"http://a","weight":2}]`)
	t.Setenv("UPSTREAM_API_KEY", "")
	accounts := loadAccounts()
	if len(accounts) != 1 || accounts[0].APIKey != "sk-a" || accounts[0].Weight != 2 {
		t.Fatalf("accounts = %+v", accounts)
	}
}
