# Tracer

This directory contains the TypeScript code that compiles to the JavaScript will
be passed as a tracer to Geth's `debug_traceCall` method.

The emitted code has a few restrictions:

- It must be a single expression which evaluates to an object with certain
  fields, as required by Geth's API.
- It must be ES5-compatible, because Matic's nodes use an old version of Node.

To meet these restrictions, we use [SWC](https://swc.rs/) to transpile our
TypeScript to ES5 JavaScript. This almost works, but falls just short of the
above restrictions, because if our input code is a single expression
`EXPRESSION`, then the emitted code appears

```
EXPRESSION;export{};
```

so can either programatically remove the last "`;export{};`" before using it as a
tracer or just edit the file (as we have done here for speed). 

Upcoming native tracers will be added and we will move away from these.

If you are developing this code, make sure you follow these rules to ensure the
above restrictions are met:

- Do not use ES6+ APIs, like `Set`, `Map`, or `Object.entries`.
- Do not use ES6+ features that will cause the transpiler to emit helper
  functions, because this breaks the "single expression" requirement. The most
  common features that would do this are array or object spread syntax.
