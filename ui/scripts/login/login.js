const password = document.getElementById('password');
const toggle = document.getElementById('password-visibility');
const email = document.getElementById('login');
const notice = document.querySelector('.session-notice:target');

if (email && notice) {
  email.setAttribute('aria-describedby', notice.id);
}

if (password && toggle) {
  const form = password.form;
  const submit = form.querySelector('button[type="submit"]');

  toggle.addEventListener('click', () => {
    const visible = password.type === 'password';
    password.type = visible ? 'text' : 'password';
    toggle.setAttribute('aria-pressed', String(visible));
  });

  form.addEventListener('submit', () => {
    submit.disabled = true;
  });

  window.addEventListener('pageshow', () => {
    submit.disabled = false;
  });
}
