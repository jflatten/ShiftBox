import fs from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

// SHARP_MODULE may point to an existing installation; otherwise run npm ci here.
const require = createRequire(import.meta.url);
const sharp = require(process.env.SHARP_MODULE || 'sharp');
const root = path.dirname(fileURLToPath(import.meta.url));
const ui = path.resolve(root, '../ui');
const source = JSON.parse(await fs.readFile(path.join(root, 'source/command-space.json'), 'utf8'));
const { mark, wordmark, colors: c } = source;
const paths = (shape, fill) => shape.paths.map(d => `<path fill="${fill}" fill-rule="evenodd" d="${d}"/>`).join('');
const svg = (w,h,body,title='ShiftBox') => `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" role="img" aria-label="${title}"><title>${title}</title>${body}</svg>\n`;
const put = async (name, data) => { const dest=path.join(root,name); await fs.mkdir(path.dirname(dest),{recursive:true}); await fs.writeFile(dest,data); };
const raster = async (name, content, width) => put(name, await sharp(Buffer.from(content)).resize({width}).png().toBuffer());
const gap=60, pad=24, lw=mark.width+gap+wordmark.width+pad*2, lh=mark.height+pad*2;
const lockupBody=(mc,wc)=>`<g transform="translate(${pad} ${pad})">${paths(mark,mc)}</g><g transform="translate(${pad+mark.width+gap} ${pad+(mark.height-wordmark.height)/2})">${paths(wordmark,wc)}</g>`;
const lockup=(mc,wc)=>svg(lw,lh,lockupBody(mc,wc));
const squareBody=(fill,ratio=.7)=>{const scale=512*ratio/Math.max(mark.width,mark.height);return `<g transform="translate(${(512-mark.width*scale)/2} ${(512-mark.height*scale)/2}) scale(${scale})">${paths(mark,fill)}</g>`;};
const square=(fill,bg,ratio=.7)=>svg(512,512,(bg?`<path fill="${bg}" d="M0 0h512v512H0z"/>`:'')+squareBody(fill,ratio));
const variants={light:[c.violet,c.ink],dark:[c.violetLight,c.white],black:['#000000','#000000'],white:[c.white,c.white]};

for (const [name,[mc,wc]] of Object.entries(variants)) {
  const logo=lockup(mc,wc);
  await put(`svg/shiftbox-logo-${name}.svg`,logo);
  await raster(`png/shiftbox-logo-${name}-1600.png`,logo,1600);
  const m=svg(mark.width+48,mark.height+48,`<g transform="translate(24 24)">${paths(mark,mc)}</g>`);
  await put(`svg/shiftbox-mark-${name}.svg`,m);
  await raster(`png/shiftbox-mark-${name}-512.png`,square(mc),512);
}
for (const [name,[mc,wc]] of Object.entries(variants).slice(0,2)) {
  await put(`svg/shiftbox-wordmark-${name}.svg`,svg(wordmark.width+48,wordmark.height+48,`<g transform="translate(24 24)">${paths(wordmark,wc)}</g>`));
  const s=.62, w=720, h=440;
  const body=`<g transform="translate(${(w-mark.width)/2} 32)">${paths(mark,mc)}</g><g transform="translate(${(w-wordmark.width*s)/2} 330) scale(${s})">${paths(wordmark,wc)}</g>`;
  const stacked=svg(w,h+40,body);
  await put(`svg/shiftbox-stacked-${name}.svg`,stacked);
  await raster(`png/shiftbox-stacked-${name}-1200.png`,stacked,1200);
}

for (const [name,fg,bg] of [['violet',c.white,c.violet],['light',c.violet,c.white],['dark',c.violetLight,c.ink]]) {
  const icon=square(fg,bg);
  await put(`svg/shiftbox-avatar-${name}.svg`,icon);
  for(const size of [512,1024]) await raster(`repository/shiftbox-avatar-${name}-${size}.png`,icon,size);
}

// A permanent violet tile keeps the small white symbol visible on either browser theme.
const favicon=svg(512,512,`<rect width="512" height="512" rx="96" fill="${c.violet}"/>`+squareBody(c.white,.76));
await put('icons/favicon.svg',favicon);
for(const size of [16,32,48]) await raster(`icons/favicon-${size}.png`,favicon,size);
const icoImages=await Promise.all([16,32,48].map(n=>fs.readFile(path.join(root,`icons/favicon-${n}.png`))));
const header=Buffer.alloc(6+16*icoImages.length);header.writeUInt16LE(1,2);header.writeUInt16LE(icoImages.length,4);
let offset=header.length;
icoImages.forEach((buffer,i)=>{const p=6+i*16,size=[16,32,48][i];header[p]=size;header[p+1]=size;header.writeUInt16LE(1,p+4);header.writeUInt16LE(32,p+6);header.writeUInt32LE(buffer.length,p+8);header.writeUInt32LE(offset,p+12);offset+=buffer.length;});
await put('icons/favicon.ico',Buffer.concat([header,...icoImages]));
await raster('icons/apple-touch-icon.png',square(c.white,c.violet),180);
for(const size of [192,512]) await raster(`icons/icon-${size}.png`,square(c.white,c.violet),size);
// All artwork is inside the central 80%-diameter circle after arbitrary launcher masks.
await raster('icons/icon-maskable-512.png',square(c.white,c.violet,.58),512);
await put('icons/site.webmanifest',JSON.stringify({name:'ShiftBox',short_name:'ShiftBox',description:'Open-source cloud development environments',start_url:'/',scope:'/',display:'standalone',background_color:c.ink,theme_color:c.violet,icons:[{src:'icon-192.png',sizes:'192x192',type:'image/png',purpose:'any'},{src:'icon-512.png',sizes:'512x512',type:'image/png',purpose:'any'},{src:'icon-maskable-512.png',sizes:'512x512',type:'image/png',purpose:'maskable'}]},null,2)+'\n');

// Social files are deliberately opaque to display consistently on repository hosts.
for(const [name,bg,mc,wc] of [['light',c.white,c.violet,c.ink],['dark',c.ink,c.violetLight,c.white]]) {
  const scale=1000/lw;
  const social=svg(1280,640,`<path fill="${bg}" d="M0 0h1280v640H0z"/><g transform="translate(140 ${320-lh*scale/2}) scale(${scale})">${lockupBody(mc,wc)}</g>`);
  await put(`repository/shiftbox-social-${name}.svg`,social);
  await raster(`repository/shiftbox-social-${name}-1280x640.png`,social,1280);
}
const palette = JSON.stringify(c, null, 2) + '\n';
await put('brand-colors.json', palette);

// Serve only the runtime assets; the guide, sources, and review board stay outside the WebUI bundle.
const publicBrand = path.join(ui, 'public', 'brand');
await fs.mkdir(publicBrand, { recursive: true });
for (const asset of [
  'svg/shiftbox-logo-light.svg', 'svg/shiftbox-logo-dark.svg',
  'svg/shiftbox-mark-light.svg', 'svg/shiftbox-mark-dark.svg',
  'icons/favicon.svg', 'icons/favicon.ico', 'icons/apple-touch-icon.png',
]) {
  await fs.copyFile(path.join(root, asset), path.join(publicBrand, path.basename(asset)));
}
await fs.mkdir(path.join(ui, 'lib'), { recursive: true });
await fs.writeFile(path.join(ui, 'lib', 'brand-colors.json'), palette);

const inset=(x,y,scale,mc,wc)=>`<g transform="translate(${x} ${y}) scale(${scale})">${lockupBody(mc,wc)}</g>`;
const preview=svg(1600,1140,`<path fill="#F5F5F8" d="M0 0h1600v1140H0z"/><text x="72" y="74" fill="${c.ink}" font-family="sans-serif" font-size="30" font-weight="700">ShiftBox / Command Space</text><text x="72" y="108" fill="#666473" font-family="sans-serif" font-size="17">Production brand kit · outlined artwork · transparent exports</text><rect x="48" y="148" width="1504" height="306" rx="20" fill="white"/>${inset(110,152,.95,c.violet,c.ink)}<rect x="48" y="478" width="1504" height="306" rx="20" fill="${c.ink}"/>${inset(110,482,.95,c.violetLight,c.white)}<rect x="48" y="808" width="1504" height="284" rx="20" fill="white"/><g transform="translate(90 836) scale(.43)"><rect width="512" height="512" rx="96" fill="${c.violet}"/>${squareBody(c.white)}</g><g transform="translate(358 836) scale(.43)"><rect width="512" height="512" rx="96" fill="${c.ink}"/>${squareBody(c.violetLight)}</g><g transform="translate(626 836) scale(.43)">${squareBody('#000000')}</g><text x="944" y="874" fill="${c.ink}" font-family="sans-serif" font-size="20" font-weight="700">Violet · Ink · White</text><rect x="944" y="900" width="100" height="70" rx="8" fill="${c.violet}"/><rect x="1064" y="900" width="100" height="70" rx="8" fill="${c.ink}"/><rect x="1184" y="900" width="100" height="70" rx="8" fill="${c.violetLight}"/><text x="944" y="1008" fill="#666473" font-family="monospace" font-size="16">#4834F5   #191821   #A599FF</text><text x="944" y="1042" fill="#666473" font-family="sans-serif" font-size="16">Logo / app icon / repository avatar</text>`,'ShiftBox brand kit preview');
await raster('shiftbox-brand-preview.png',preview,1600);
console.log('Generated ShiftBox SVG, PNG, ICO, manifest, repository and WebUI assets.');
