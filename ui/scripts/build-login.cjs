// Dex expands the form action, retry email, and login state at runtime.
// Render Mantine at build time so the credential form needs no Node server.
const { copyFileSync, mkdirSync, writeFileSync } = require('node:fs');
const { join } = require('node:path');
const { createElement: h } = require('react');
const { renderToStaticMarkup } = require('react-dom/server');
const { Alert, Anchor, Button, ColorSchemeScript, Container, Group, Image, MantineProvider, PasswordInput, Stack, TextInput, Title, createTheme, mantineHtmlProps } = require('@mantine/core');

const root = join(__dirname, '../out/dex/frontend');
const templateDir = join(root, 'templates');
const themeDir = join(root, 'themes/light');
const theme = createTheme({ primaryColor: 'violet', defaultRadius: 'sm', fontFamily: 'system-ui, sans-serif' });
const asset = '/dex/theme/';

function page(children) {
  return '<!doctype html>' + renderToStaticMarkup(
    h('html', { lang: 'en', ...mantineHtmlProps },
      h('head', null,
        h('meta', { charSet: 'utf-8' }),
        h('meta', { name: 'viewport', content: 'width=device-width, initial-scale=1' }),
        h('meta', { name: 'referrer', content: 'no-referrer' }),
        h('title', null, 'Log in to ShiftBox'),
        h('link', { rel: 'icon', href: asset + 'favicon.svg' }),
        h('link', { rel: 'stylesheet', href: asset + 'mantine.css' }),
        h('link', { rel: 'stylesheet', href: asset + 'login.css' }),
        h(ColorSchemeScript, { defaultColorScheme: 'auto' }),
      ),
      h('body', null,
        h(MantineProvider, { theme, defaultColorScheme: 'auto' },
          h('header', null,
            h(Image, { className: 'brand-light', src: asset + 'shiftbox-logo-light.svg', alt: 'ShiftBox', w: 160, h: 40, fit: 'contain' }),
            h(Image, { className: 'brand-dark', src: asset + 'shiftbox-logo-dark.svg', alt: 'ShiftBox', w: 160, h: 40, fit: 'contain' }),
          ),
          h(Container, { component: 'main', size: 448, className: 'login' }, children),
        ),
        h('script', { src: asset + 'login.js', defer: true }),
      ),
    ),
  );
}

const login = page(h(Stack, { gap: 24 },
  h(Title, { order: 1, fw: 400 }, 'Log in to ShiftBox'),
  h(Alert, { id: 'session-expired', className: 'session-notice', color: 'yellow' }, 'Your session expired. Log in again to continue.'),
  h(Alert, { id: 'session-ended', className: 'session-notice', color: 'yellow' }, 'Your session ended. Log in again to continue.'),
  h(Alert, { id: 'sign-in-retry', className: 'session-notice', color: 'yellow' }, 'We couldn’t complete your login. Please try again.'),
  h('form', { method: 'post', action: '/SHIFTBOX_POST_URL' },
    h(Stack, { gap: 16 },
      h(TextInput, { id: 'login', label: 'Email', name: 'login', type: 'email', autoComplete: 'username', autoCapitalize: 'none', spellCheck: false, required: true, withAsterisk: false, autoFocus: true, defaultValue: 'SHIFTBOX_EMAIL' }),
      h(PasswordInput, { id: 'password', label: 'Password', name: 'password', autoComplete: 'current-password', required: true, withAsterisk: false, visibilityToggleFocusable: true, visibilityToggleButtonProps: { id: 'password-visibility', 'aria-label': 'Show password', 'aria-controls': 'password', 'aria-pressed': false } }),
      'SHIFTBOX_ERROR',
      h(Group, null, h(Button, { type: 'submit', size: 'md' }, 'Log in')),
    ),
  ),
  'SHIFTBOX_LOGIN_OPTIONS',
));
const error = renderToStaticMarkup(h(MantineProvider, { env: 'test' },
  h(Alert, { color: 'red', variant: 'light', role: 'alert' }, 'Incorrect email or password. Try again.'),
));
const loginOptions = renderToStaticMarkup(h(MantineProvider, { env: 'test' },
  h(Anchor, { href: '/SHIFTBOX_LOGIN_OPTIONS_URL', size: 'sm' }, 'Other login options'),
)).replace('/SHIFTBOX_LOGIN_OPTIONS_URL', '{{ extra "loginOptionsURL" }}');

const retry = page(h(Stack, { gap: 24 },
  h(Title, { order: 1, fw: 400 }, 'Log in to ShiftBox'),
  h(Alert, { color: 'red', variant: 'light', role: 'alert' }, 'We couldn’t complete your login. Start again.'),
  h(Anchor, { href: '/SHIFTBOX_RETRY_URL' }, 'Return to login'),
)).replace('/SHIFTBOX_RETRY_URL', '{{ extra "retryURL" }}');

mkdirSync(templateDir, { recursive: true });
mkdirSync(themeDir, { recursive: true });
writeFileSync(join(root, 'robots.txt'), 'User-agent: *\nDisallow: /\n');
writeFileSync(join(templateDir, 'password.html'), login
  .replace('/SHIFTBOX_POST_URL', '{{ .PostURL }}')
  .replace('SHIFTBOX_EMAIL', '{{ .Username }}')
  .replace('SHIFTBOX_ERROR', `{{ if .Invalid }}${error}{{ end }}`)
  .replace('SHIFTBOX_LOGIN_OPTIONS', `{{ if extra "loginOptionsURL" }}${loginOptions}{{ end }}`));
writeFileSync(join(templateDir, 'error.html'), retry);
// The managed authorization-code client selects its connector explicitly.
// All other pages Dex requires at startup share the branded recovery screen.
for (const name of ['login', 'approval', 'oob', 'device', 'device_success', 'totp_verify', 'webauthn_verify', 'home', 'logout']) {
  writeFileSync(join(templateDir, name + '.html'), `{{ template "error.html" . }}`);
}
copyFileSync(require.resolve('@mantine/core/styles.css'), join(themeDir, 'mantine.css'));
for (const name of ['favicon.svg', 'shiftbox-logo-light.svg', 'shiftbox-logo-dark.svg']) {
  copyFileSync(join(__dirname, '../public/brand', name), join(themeDir, name));
}
for (const name of ['login.css', 'login.js']) {
  copyFileSync(join(__dirname, 'login', name), join(themeDir, name));
}
