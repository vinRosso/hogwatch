# Administration

Admins get a **Settings** section.

## Users

Create users, give each one paths to see and an optional quota. A user only ever sees the folders assigned to them; anything else answers "not found". Usage and growth for every user are ranked here.

![The Users page](/images/admin-users.png)

## Mounts

Roots are the folders Hogwatch scans. The page lists what the scanner can see, with allocation, free space and growth.

![The Mounts page](/images/admin-mounts.png)

## Scans

Start scans, watch progress and see history. Schedules are cron expressions per root. A new scan replaces the old one only when it finishes, so readers never see a half-built tree.

![The Scans page](/images/admin-scans.png)

## Config issues

Lists problems such as a path that no longer exists, a writable mount or a missing scanner.

![Config issues](/images/admin-issues.png)

## Categories and Settings

Categories map extensions to the groups used by the treemap and filters. Settings holds retention and listing options.

![Categories](/images/admin-categories.png)

![Settings](/images/admin-settings.png)

## Audit

Every sensitive action is recorded, including file listings.

![The audit log](/images/admin-audit.png)
