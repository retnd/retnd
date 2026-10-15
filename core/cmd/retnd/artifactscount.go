package main

import "strconv"

// `artifacts --count` (#1186): the terminal's equivalent of the web
// console's sidebar Backups badge.
//
// The badge reads the number of retained backups, exact up to 100 and ">100"
// above it. This counts the rows `artifacts` lists, after --state, and
// applies the same cap. The exact figure is never hidden: plain `artifacts`
// ends with "N artifact(s)", and the length of `artifacts --json`'s array is
// the same number.

// retainedBadgeCap is the most the Backups badge in the web console spells
// out (ui/shared AppShell, BACKUPS_BADGE_CAP). Past it the badge reads ">100".
const retainedBadgeCap = 100

// retainedBadge is the reading the console's sidebar badge shows for n
// retained backups.
func retainedBadge(n int) string {
	if n > retainedBadgeCap {
		return ">" + strconv.Itoa(retainedBadgeCap)
	}
	return strconv.Itoa(n)
}
