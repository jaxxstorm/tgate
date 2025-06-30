function ui() {
  return {
    subtitle: 'live request log',
    requests: [],
    selected: null,

    init() {
      // initial fetch + 1-s polling
      this.poll()
      setInterval(() => this.poll(), 1_000)
    },

    poll() {
      fetch('api/requests')
        .then(r => r.json())
        .then(data => {              // newest first
          this.requests = data.slice().reverse()
        })
        .catch(() => {})             // ignore network blips
    },

    select(r) { this.selected = r }
  }
}
