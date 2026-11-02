---
layout: home
hero:
  name: Hogwatch
  text: See where your disk space went
  tagline: A self-hosted disk usage analyzer with a treemap, history and per-user quotas. One Docker Compose file.
  image:
    src: /images/browse-treemap.png
    alt: The Hogwatch overview, a treemap of one folder with a file list and a size history chart
  actions:
    - theme: brand
      text: Install
      link: /install
    - theme: alt
      text: Using Hogwatch
      link: /using
features:
  - title: Treemap and list
    details: Click into folders, filter by category, age or size, and export what you see.
  - title: History
    details: Every scan is kept, so you can see which folders grew and by how much.
  - title: Safe by design
    details: The web part never touches your files. A separate read-only scanner does, with almost no privileges.
  - title: ZFS aware
    details: Optional sidecar shows dataset quotas and snapshot space next to the scan.
---
