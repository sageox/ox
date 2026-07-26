// ox plan — portable scaffold runtime. No build step, runs from file://.
// Mermaid is the only external dep (CDN, at view time). Handles dark/light
// re-render, OS-preference default + persistence, sticky section jumps
// (plans with >3 sections), and scroll-spy TOC.
(function(){
  var darkVars={background:'#111411',primaryColor:'#171b17',primaryTextColor:'#e8ede7',primaryBorderColor:'#252c24',lineColor:'#6f7a6d',secondaryColor:'#141814',tertiaryColor:'#121612',fontFamily:'Inter, sans-serif',fontSize:'13px',actorBkg:'#171b17',actorBorder:'#252c24',actorTextColor:'#e8ede7',noteBkgColor:'#241803',noteTextColor:'#f7d8a0',noteBorderColor:'#f59e0b'};
  var lightVars={background:'#ffffff',primaryColor:'#f5f8f4',primaryTextColor:'#16201c',primaryBorderColor:'#d8e0d8',lineColor:'#6c7a72',secondaryColor:'#eef4ee',tertiaryColor:'#eef7f5',fontFamily:'Inter, sans-serif',fontSize:'13px',actorBkg:'#f5f8f4',actorBorder:'#d8e0d8',actorTextColor:'#16201c',noteBkgColor:'#fdf2dc',noteTextColor:'#6b4a0c',noteBorderColor:'#b4730c'};
  var nodes=[].slice.call(document.querySelectorAll('.mermaid'));
  var srcs=nodes.map(function(n){return n.textContent;});
  function renderMer(){
    if(typeof mermaid==='undefined')return;
    var dark=document.documentElement.getAttribute('data-theme')!=='light';
    nodes.forEach(function(n,i){n.removeAttribute('data-processed');n.textContent=srcs[i];});
    mermaid.initialize({startOnLoad:false,theme:'base',themeVariables:dark?darkVars:lightVars,flowchart:{nodeSpacing:34,rankSpacing:34,padding:8,useMaxWidth:true},sequence:{useMaxWidth:true},state:{useMaxWidth:true},securityLevel:'antiscript'});
    try{mermaid.run({nodes:document.querySelectorAll('.mermaid')});}catch(e){}
  }
  var root=document.documentElement,btn=document.getElementById('themeBtn');
  var saved=null;try{saved=localStorage.getItem('ox-plan-theme');}catch(e){}
  if(saved)root.setAttribute('data-theme',saved);
  else if(window.matchMedia&&window.matchMedia('(prefers-color-scheme: light)').matches)root.setAttribute('data-theme','light');
  if(btn)btn.onclick=function(){var next=root.getAttribute('data-theme')==='light'?'dark':'light';root.setAttribute('data-theme',next);try{localStorage.setItem('ox-plan-theme',next);}catch(e){}renderMer();};
  var links=[].slice.call(document.querySelectorAll('nav.toc a'));
  // Sticky section jumps (>3 sections): every section stays in the document
  // flow so reviewers keep context and review-rail jumps can always land on
  // visible content. The bar is a compact duplicate of the side TOC for long
  // plans, not an alternate one-section app state.
  var tabbar=document.querySelector('.tabbar');
  var activate=function(id,remember){
    if(!id)return;
    if(tabbar){
      var tabs=[].slice.call(tabbar.querySelectorAll('button[data-tab]'));
      tabs.forEach(function(b){b.setAttribute('aria-current',String(b.getAttribute('data-tab')===id));});
    }
    links.forEach(function(l){l.classList.toggle('active',l.getAttribute('href')==='#'+id);});
    if(remember&&history.replaceState)history.replaceState(null,'','#'+id);
  };
  if(tabbar){
    document.body.classList.add('has-tabbar'); // the jump bar is the one nav — CSS hides the duplicate TOC
    [].slice.call(tabbar.querySelectorAll('button[data-tab]')).forEach(function(b){
      b.addEventListener('click',function(){
        var id=b.getAttribute('data-tab'),el=document.getElementById(id);
        activate(id,true);
        if(el)el.scrollIntoView({behavior:'smooth',block:'start'});
      });
    });
    var initial=(location.hash||'').slice(1);
    if(initial&&document.getElementById(initial))activate(initial,false);
  }
  // scroll-spy over section headings. In long-plan mode it drives both the
  // side TOC and the sticky jump bar.
  if(window.IntersectionObserver){
    var obs=new IntersectionObserver(function(es){es.forEach(function(e){if(e.isIntersecting){activate(e.target.id,false);}});},{rootMargin:'-12% 0px -75% 0px'});
    document.querySelectorAll('section[id]').forEach(function(s){obs.observe(s);});
  }
  function start(){renderMer();}
  if(document.readyState!=='loading')start();else document.addEventListener('DOMContentLoaded',start);
})();
// auto-inspector: comparison tables (table.inspect) — click a row, its fields
// project into the docked explainer with header-labeled values.
(function(){
  [].slice.call(document.querySelectorAll('table.inspect')).forEach(function(t){
    var dock=t.nextElementSibling;
    if(!dock||(dock.className||'').indexOf('inspect-dock')<0)return;
    var body=dock.querySelector('.inspect-dock-body');
    var heads=[].slice.call(t.querySelectorAll('thead th')).map(function(h){return h.textContent.trim();});
    [].slice.call(t.querySelectorAll('tbody tr')).forEach(function(tr){
      tr.addEventListener('click',function(){
        [].slice.call(t.querySelectorAll('tr.lit')).forEach(function(r){r.classList.remove('lit');});
        tr.classList.add('lit');
        if(body){
          body.textContent='';
          [].slice.call(tr.children).forEach(function(c,i){
            var row=document.createElement('div');row.className='inspect-f';
            var k=document.createElement('span');k.className='inspect-k';
            k.textContent=heads[i]||('field '+(i+1));
            var v=document.createElement('span');v.className='inspect-v';
            v.innerHTML=c.innerHTML; // first-party rendered cell markup
            row.appendChild(k);row.appendChild(v);body.appendChild(row);
          });
        }
        var hint=dock.querySelector('.inspect-dock-hint');
        if(hint)hint.remove();
        dock.hidden=false;
      });
    });
  });
})();
// keyboard map: 1-9 jump to tab, [ / ] prev-next tab, t theme, r review toggle.
// Skipped while typing in an input/textarea (review notes).
(function(){
  function typing(e){var t=e.target;return t&&(t.tagName==='TEXTAREA'||t.tagName==='INPUT'||t.isContentEditable);}
  document.addEventListener('keydown',function(e){
    if(typing(e)||e.metaKey||e.ctrlKey||e.altKey)return;
    var tabs=[].slice.call(document.querySelectorAll('.tabbar button[data-tab]'));
    if(e.key==='t'){var b=document.getElementById('themeBtn');if(b){b.click();e.preventDefault();}return;}
    if(e.key==='r'){var r=document.querySelector('.rev-bar .rev-toggle');if(r){r.click();e.preventDefault();}return;}
    if(!tabs.length)return;
    var cur=tabs.findIndex(function(b){return b.getAttribute('aria-current')==='true';});
    if(e.key>='1'&&e.key<='9'){var i=+e.key-1;if(tabs[i]){tabs[i].click();e.preventDefault();}return;}
    if(e.key==='['&&cur>0){tabs[cur-1].click();e.preventDefault();return;}
    if(e.key===']'&&cur>=0&&cur<tabs.length-1){tabs[cur+1].click();e.preventDefault();return;}
  });
})();
