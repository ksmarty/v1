const sharp = require('sharp');
const src = process.argv[2];
const outDir = process.argv[3];

async function main() {
  const svg = require('fs').readFileSync(src);
  const render = (size) => sharp(svg, { density: 300 }).resize(size, size).png().toBuffer();

  const [p192, p512, pApple] = await Promise.all([
    render(192),
    render(512),
    render(180),
  ]);
  require('fs').writeFileSync(`${outDir}/icon-192.png`, p192);
  require('fs').writeFileSync(`${outDir}/icon-512.png`, p512);
  require('fs').writeFileSync(`${outDir}/apple-touch-icon.png`, pApple);
  // The icon is full-bleed (the tile fills the whole canvas, glyph sits in
  // the central ~80%), so the 512 PNG doubles as the maskable asset.
  require('fs').writeFileSync(`${outDir}/icon-maskable-512.png`, p512);
  console.log('generated', p192.length, p512.length, pApple.length);
}
main().catch((e) => { console.error(e); process.exit(1); });
