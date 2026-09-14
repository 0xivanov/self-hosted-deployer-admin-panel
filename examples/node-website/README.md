# Node.js website demo

A small website with no external packages. Node 24 builds `dist/index.html` and
serves it on the runtime-assigned `PORT`, with `/health` for readiness.

ZIP `package.json`, `package-lock.json`, `build.js`, `server.js` and `index.html`
at the archive root. Upload the ZIP to a Node project, choose **Build**, refresh
until the release is ready, then choose **Deploy**. The project needs an assigned
builder and runtime. A saved build alone does not make a website live.

Edit `index.html` to customize the page. Uploading and deploying a new version
creates a new release; selecting an older saved release uses the rollback flow.
