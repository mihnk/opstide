// OpsTide website: copy buttons and the small-screen menu. The page works without it.
(function () {
  'use strict'
  var root = document.documentElement
  root.classList.add('js')

  function init() {
    // Copy buttons: data-copy names the element whose text is copied.
    var status = document.getElementById('copy-status')
    var copied = root.getAttribute('data-copied') || 'Copied'
    var failed = root.getAttribute('data-copy-failed') || 'Copy failed'
    function say(text) {
      if (!status) return
      status.textContent = ''
      setTimeout(function () { status.textContent = text }, 30)
    }
    function fallbackCopy(text) {
      var ta = document.createElement('textarea')
      ta.value = text
      ta.setAttribute('readonly', '')
      ta.className = 'sr-only'
      document.body.appendChild(ta)
      ta.select()
      var ok = false
      try { ok = document.execCommand('copy') } catch (e) { ok = false }
      document.body.removeChild(ta)
      return ok
    }
    Array.prototype.forEach.call(document.querySelectorAll('button[data-copy]'), function (btn) {
      btn.addEventListener('click', function () {
        var src = document.getElementById(btn.getAttribute('data-copy'))
        if (!src) return
        var text = src.textContent.replace(/\s+$/, '')
        var done = function () {
          btn.classList.add('is-done')
          say(copied)
          setTimeout(function () { btn.classList.remove('is-done') }, 2000)
        }
        if (navigator.clipboard && window.isSecureContext) {
          navigator.clipboard.writeText(text).then(done, function () { fallbackCopy(text) ? done() : say(failed) })
        } else {
          fallbackCopy(text) ? done() : say(failed)
        }
      })
    })

    // Small-screen menu.
    var toggle = document.querySelector('.nav-toggle')
    var nav = document.getElementById('site-nav')
    if (toggle && nav) {
      var set = function (open) {
        toggle.setAttribute('aria-expanded', open ? 'true' : 'false')
        nav.classList.toggle('is-open', open)
      }
      toggle.addEventListener('click', function () { set(toggle.getAttribute('aria-expanded') !== 'true') })
      nav.addEventListener('click', function (e) { if (e.target.closest('a')) set(false) })
      document.addEventListener('keydown', function (e) {
        if (e.key === 'Escape' && toggle.getAttribute('aria-expanded') === 'true') { set(false); toggle.focus() }
      })
    }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init)
  else init()
})()
