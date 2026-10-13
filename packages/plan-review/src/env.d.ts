// build.mjs loads a .css import as its text.
declare module '*.css' {
  const text: string;
  export default text;
}
