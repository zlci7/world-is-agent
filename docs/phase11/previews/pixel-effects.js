(() => {
  'use strict';
  const games = {'stardew-valley':'farm',rimworld:'colony'};
  const scenery = document.createElement('div');
  scenery.className = 'pixel-scenery';
  scenery.setAttribute('aria-hidden','true');
  scenery.innerHTML = Object.entries(games).map(([id,scene])=>`<div class="pixel-scene ${scene}" data-scene="${id}"></div>`).join('');
  document.body.prepend(scenery);

  function syncScene() {
    const current = document.body.dataset.game;
    scenery.querySelectorAll('[data-scene]').forEach(layer=>layer.classList.toggle('visible',layer.dataset.scene===current));
    const select = document.getElementById('preview-game');
    if(select)select.value=current;
  }

  document.addEventListener('preview:request-game',event=>{
    const game=event.detail?.game;
    if(!Object.hasOwn(games,game))return;
    if(game!==document.body.dataset.game)document.dispatchEvent(new CustomEvent('preview:commit-game',{detail:{game}}));
    syncScene();
    const control=event.detail?.source==='settings'?document.querySelector('#game-form button[type=submit]'):document.getElementById('preview-game');
    control?.focus({preventScroll:true});
  });
  document.addEventListener('preview:render',syncScene);
  syncScene();
})();
