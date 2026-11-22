export function providerLabel(kind?: string) {
  if (kind === 'ec2') return 'AWS EC2';
  if (kind === 'utm') return 'UTM';
  return kind || 'Unavailable';
}

export function platformImageLabel(image: string, provider?: string) {
  return provider ? `${providerLabel(provider)} · ${image}` : image;
}
