// Pixel art for the office, drawn by hand for Shiplino: each sprite is a
// grid of palette keys ("." is transparent). Sprites are rendered once per
// palette into a small canvas and reused every frame.

export type Sprite = readonly string[];
export type Colors = Record<string, string>;

// Standing, facing you. h hair, s skin, e eyes, c shirt, d belt, p trousers, k shoes.
const standTop = [
  "...hhhhhh...",
  "..hhhhhhhh..",
  "..hhhhhhhh..",
  "..hssssssh..",
  "..sesssses..",
  "..ssssssss..",
  "...ssssss...",
  "....ssss....",
  "..cccccccc..",
  ".cccccccccc.",
];
const body = [".sccccccccs.", ".sccccccccs.", "..dddddddd.."];
const legs = ["..pppppppp..", "..ppp..ppp..", "..ppp..ppp..", "..ppp..ppp..", "..kkk..kkk.."];
const stepL = ["..pppppppp..", "..ppp..ppp..", "..ppp..ppp..", "..kkk..ppp..", ".......kkk.."];
const stepR = ["..pppppppp..", "..ppp..ppp..", "..ppp..ppp..", "..ppp..kkk..", "..kkk......."];

export const standing: Sprite = [...standTop, ...body, ...legs, "............"];
export const walkL: Sprite = [...standTop, ...body, ...stepL, "............"];
export const walkR: Sprite = [...standTop, ...body, ...stepR, "............"];
/** Arms up, two frames, for a finished task. */
export const cheer1: Sprite = [
  "s..hhhhhh..s",
  "s.hhhhhhhh.s",
  "c.hhhhhhhh.c",
  "c.hssssssh.c",
  "c.sesssses.c",
  "c.ssssssss.c",
  "cc.ssssss.cc",
  ".c..ssss..c.",
  ".cccccccccc.",
  "..cccccccc..",
  "..cccccccc..",
  "..cccccccc..",
  "..dddddddd..",
  ...legs,
  "............",
];
export const cheer2: Sprite = ["............", ...cheer1.slice(0, -1)];

// Seated, seen from behind (the desk is in front of them).
export const seated: Sprite = [
  "...hhhhhh...",
  "..hhhhhhhh..",
  "..hhhhhhhh..",
  "..hhhhhhhh..",
  "..hhhhhhhh..",
  "...hhhhhh...",
  "....ssss....",
  "..cccccccc..",
  ".cccccccccc.",
  ".cccccccccc.",
  ".cccccccccc.",
  "..dddddddd..",
];
/** Subagents wear a cap in their agent's color. */
export const cap: Sprite = ["...cccccc...", "..cccccccc.."];

// Office furniture. w frame, x screen, t stand, b chair, o chair dark.
export const monitor: Sprite = [
  "wwwwwwwwwwwwwwwwww",
  "wxxxxxxxxxxxxxxxxw",
  "wxxxxxxxxxxxxxxxxw",
  "wxxxxxxxxxxxxxxxxw",
  "wxxxxxxxxxxxxxxxxw",
  "wxxxxxxxxxxxxxxxxw",
  "wxxxxxxxxxxxxxxxxw",
  "wxxxxxxxxxxxxxxxxw",
  "wxxxxxxxxxxxxxxxxw",
  "wxxxxxxxxxxxxxxxxw",
  "wwwwwwwwwwwwwwwwww",
  ".......tttt.......",
  "......tttttt......",
];
export const chair: Sprite = [
  "..bbbbbbbbbbbb..",
  ".bbbbbbbbbbbbbb.",
  ".bbbbbbbbbbbbbb.",
  ".oooooooooooooo.",
  "...o........o...",
  "..oo........oo..",
];
/** The "needs you" bell on its stand. y bell, Y shine, t stand. */
export const bell: Sprite = [
  "....YY....",
  "...yyyy...",
  "..yYyyyy..",
  "..yYyyyy..",
  ".yyyyyyyy.",
  "yyyyyyyyyy",
  "....tt....",
  "....tt....",
  "....tt....",
  "..tttttt..",
];
export const plant: Sprite = [
  "..g..g..",
  ".ggg.gg.",
  "gg.ggg.g",
  ".ggGgg..",
  "..gGg...",
  ".rrrrrr.",
  ".rrrrrr.",
  "..rrrr..",
];

// Glyphs, 3-5 pixels wide, in "#".
export const glyphs: Record<string, Sprite> = {
  "!": ["#", "#", "#", ".", "#"],
  "?": ["##.", "..#", ".#.", "...", ".#."],
  z: ["###", "..#", ".#.", "#..", "###"],
  dots: ["#.#.#"],
  check: ["....#", "...#.", "#.#..", ".#..."],
};

const cache = new Map<string, HTMLCanvasElement>();

/** render draws a sprite at 1x into a cached canvas. */
function render(sprite: Sprite, colors: Colors): HTMLCanvasElement {
  const key = `${sprite.join("/")}|${Object.entries(colors).join(",")}`;
  let c = cache.get(key);
  if (c) return c;
  c = document.createElement("canvas");
  c.width = Math.max(...sprite.map((r) => r.length));
  c.height = sprite.length;
  const ctx = c.getContext("2d")!;
  sprite.forEach((row, y) => {
    for (let x = 0; x < row.length; x++) {
      const color = colors[row[x]!];
      if (!color) continue;
      ctx.fillStyle = color;
      ctx.fillRect(x, y, 1, 1);
    }
  });
  if (cache.size > 2000) cache.clear(); // bounded: theme switches make new palettes
  cache.set(key, c);
  return c;
}

/** draw puts a sprite with its top-left at (x, y), optionally mirrored. */
export function draw(ctx: CanvasRenderingContext2D, sprite: Sprite, colors: Colors, x: number, y: number) {
  ctx.drawImage(render(sprite, colors), Math.round(x), Math.round(y));
}

/** glyph draws a glyph in one color. */
export function glyph(ctx: CanvasRenderingContext2D, name: string, color: string, x: number, y: number) {
  const g = glyphs[name];
  if (g) draw(ctx, g, { "#": color }, x, y);
}

/** Looks: picked per session from its id, so a character keeps its look. */
export const hairs = ["#2b2118", "#5b3a22", "#c99a52", "#8c3b28", "#1c2433", "#cfc8bd", "#6a4b8c"];
export const skins = ["#f2c9a1", "#dba379", "#ad7349", "#73492f", "#f7dfc8"];
export const trousers = ["#334155", "#3f3a52", "#4a3f35", "#2f4a46"];
