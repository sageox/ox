import { compile } from '@mdx-js/mdx'
import { readdir, readFile } from 'node:fs/promises'
import { join } from 'node:path'
import remarkFrontmatter from 'remark-frontmatter'

const dir = process.argv[2] ?? 'reference'
const pages = (await readdir(dir, { recursive: true }))
  .filter(path => path.endsWith('.mdx'))
  .sort()
if (pages.length === 0) {
  throw new Error(`No generated MDX pages under ${dir}`)
}

let failures = 0
for (const path of pages) {
  const value = await readFile(join(dir, path), 'utf8')
  try {
    await compile({ path, value }, { remarkPlugins: [remarkFrontmatter] })
  } catch (error) {
    failures++
    console.error(String(error))
  }
}
console.log(`Compiled ${pages.length - failures} of ${pages.length} CLI reference pages.`)
process.exitCode = failures > 0 ? 1 : 0
