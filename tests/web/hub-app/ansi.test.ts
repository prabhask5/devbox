import { describe, expect, test } from "vitest";
import { ansiToHtml } from "@devbox/hub-app/ansi.ts";

const ESC = "\x1b";

describe("the screen renderer", () => {
  test("plain text is escaped", () => {
    expect(ansiToHtml(`<b>&"'</b>`)).toBe("&lt;b&gt;&amp;&quot;&#39;&lt;/b&gt;");
  });

  test("the 16 colours, normal and bright, as text and background", () => {
    expect(ansiToHtml(`${ESC}[31mred${ESC}[0m plain`)).toBe(
      '<span style="color:#ff6b6b">red</span> plain',
    );
    expect(ansiToHtml(`${ESC}[92mok${ESC}[m`)).toBe('<span style="color:#7ee2a8">ok</span>');
    expect(ansiToHtml(`${ESC}[44;97mx`)).toBe(
      '<span style="color:#f2f4f7;background:#6cb6ff">x</span>',
    );
    expect(ansiToHtml(`${ESC}[103mx`)).toBe('<span style="background:#ffd479">x</span>');
  });

  test("every attribute, and its reset", () => {
    expect(ansiToHtml(`${ESC}[1mb`)).toBe('<span style="font-weight:700">b</span>');
    expect(ansiToHtml(`${ESC}[2md`)).toBe('<span style="opacity:.55">d</span>');
    expect(ansiToHtml(`${ESC}[3mi`)).toBe('<span style="font-style:italic">i</span>');
    expect(ansiToHtml(`${ESC}[4mu`)).toBe('<span style="text-decoration:underline">u</span>');
    expect(ansiToHtml(`${ESC}[9ms`)).toBe('<span style="text-decoration:line-through">s</span>');
    expect(ansiToHtml(`${ESC}[4;9mx`)).toBe(
      '<span style="text-decoration:underline line-through">x</span>',
    );
    expect(ansiToHtml(`${ESC}[7minverse`)).toBe(
      '<span style="color:#090b0d;background:#d7dae0">inverse</span>',
    );
    expect(ansiToHtml(`${ESC}[31;7mx`)).toBe(
      '<span style="color:#090b0d;background:#ff6b6b">x</span>',
    );
    expect(ansiToHtml(`${ESC}[1;3;4;7;9ma${ESC}[22;23;24;27;29mb`)).toBe(
      '<span style="color:#090b0d;background:#d7dae0;font-weight:700;font-style:italic;text-decoration:underline line-through">a</span>b',
    );
    expect(ansiToHtml(`${ESC}[31;42ma${ESC}[39mb${ESC}[49mc`)).toBe(
      '<span style="color:#ff6b6b;background:#5fd38d">a</span><span style="background:#5fd38d">b</span>c',
    );
  });

  test("256 colours and true colour", () => {
    expect(ansiToHtml(`${ESC}[38;5;9mx`)).toBe('<span style="color:#ff8787">x</span>');
    expect(ansiToHtml(`${ESC}[38;5;196mx`)).toBe('<span style="color:rgb(255,0,0)">x</span>');
    expect(ansiToHtml(`${ESC}[48;5;244mx`)).toBe(
      '<span style="background:rgb(128,128,128)">x</span>',
    );
    expect(ansiToHtml(`${ESC}[38;2;1;2;3;48;2;4;5;6mx`)).toBe(
      '<span style="color:rgb(1,2,3);background:rgb(4,5,6)">x</span>',
    );
    expect(ansiToHtml(`${ESC}[38:2:10:20:30mx`)).toBe('<span style="color:rgb(10,20,30)">x</span>');
  });

  test("other sequences are dropped", () => {
    expect(ansiToHtml(`a${ESC}[2Kb${ESC}[10;5Hc${ESC}]0;title\x07d${ESC}(Be${ESC}=f`)).toBe(
      "abcdef",
    );
  });

  test("blank lines at the end are trimmed, also when they carry colours", () => {
    expect(ansiToHtml(`one\n\ntwo\n\n   \n${ESC}[0m\n`)).toBe("one\n\ntwo");
    expect(ansiToHtml("\n\n")).toBe("");
  });
});
