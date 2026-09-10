import {mkdirSync,copyFileSync} from 'node:fs';
mkdirSync('dist',{recursive:true});
copyFileSync('server.mjs','dist/server.mjs');
